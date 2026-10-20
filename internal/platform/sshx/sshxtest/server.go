// Package sshxtest is an SSH server for tests: it listens on 127.0.0.1, has
// host keys, accepts one authorized key, runs scripted commands, serves SFTP
// on a temporary directory and can act as a jump host. No test touches the
// network.
package sshxtest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Handler is a scripted command: it reads stdin, writes stdout and stderr and
// returns the exit code.
type Handler func(stdin io.Reader, stdout, stderr io.Writer) int

// NoExitStatus, returned as a command's exit code, ends its session without
// one, as a session the client's connection lost under it ends.
const NoExitStatus = -1

// Session is one command a client asked for, with who asked.
type Session struct {
	User    string
	Command string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// Freeze freezes this session's connection only, until the server
	// closes: as Server.Freeze, while other connections go on.
	Freeze func()
}

type matcher struct {
	match func(user, cmd string) bool
	fn    func(*Session) int
}

// Server is an SSH server on 127.0.0.1.
type Server struct {
	// Addr is "127.0.0.1:port".
	Addr string

	t   testing.TB
	dir string

	mu         sync.Mutex
	keys       map[string][]ssh.PublicKey // by user; "*" is any user
	passwords  map[string]string
	blocked    map[string]bool // users whose logins fail ("*": everyone)
	hostKeys   []ssh.Signer
	listeners  map[string]net.Listener // by address
	ownKeys    map[string][]ssh.Signer // host keys of an address that has its own
	handlers   map[string]Handler
	matchers   []matcher
	forwarding bool
	refusing   bool // new connections are closed at once
	noGlobal   bool // a global request closes the connection
	noUploads  bool // SFTP writes fail
	onUpload   func(path string)
	thaw       chan struct{} // non-nil while frozen; closed by Freeze(false)
	closed     chan struct{}
	conns      map[net.Conn]struct{}
	wg         sync.WaitGroup
}

// NewServer starts a server that accepts the authorized key (for any user; nil
// accepts none) and closes with the test. Its host key is a fresh ed25519 key.
func NewServer(t testing.TB, authorized ssh.PublicKey) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr: ln.Addr().String(), t: t, dir: t.TempDir(),
		keys: map[string][]ssh.PublicKey{}, passwords: map[string]string{}, blocked: map[string]bool{},
		handlers: map[string]Handler{}, conns: map[net.Conn]struct{}{}, closed: make(chan struct{}),
		listeners: map[string]net.Listener{}, ownKeys: map[string][]ssh.Signer{},
	}
	if authorized != nil {
		s.AuthorizeKey("*", authorized)
	}
	s.SetHostKeyTypes("ed25519")
	s.serveOn(ln)
	t.Cleanup(s.Close)
	return s
}

// Listen makes the server answer on one more address, a free port of
// 127.0.0.1, as sshd does once its configuration names another port, and
// returns it. Addr does not change.
func (s *Server) Listen() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	s.serveOn(ln)
	return ln.Addr().String()
}

// Unlisten stops answering at addr (Addr or one of Listen's): a new
// connection there is refused, as at a port nothing listens on, while open
// ones go on.
func (s *Server) Unlisten(addr string) {
	s.mu.Lock()
	ln := s.listeners[addr]
	delete(s.listeners, addr)
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// ForeignHostKey gives addr a fresh host key of its own, as another machine
// answering there would have; the other addresses keep the server's.
func (s *Server) ForeignHostKey(addr string) {
	sg := newSigner(s.t, "ed25519")
	s.mu.Lock()
	s.ownKeys[addr] = []ssh.Signer{sg}
	s.mu.Unlock()
}

func (s *Server) serveOn(ln net.Listener) {
	addr := ln.Addr().String()
	s.mu.Lock()
	s.listeners[addr] = ln
	s.mu.Unlock()
	s.wg.Add(1)
	go s.accept(ln, addr)
}

// Handle scripts a command, matched on its full text.
func (s *Server) Handle(cmd string, fn func(stdin io.Reader, stdout, stderr io.Writer) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[cmd] = fn
}

// HandleFunc scripts every command match accepts, after the exact ones of
// Handle, in the order added. match sees the user and the full command.
func (s *Server) HandleFunc(match func(user, cmd string) bool, fn func(*Session) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.matchers = append(s.matchers, matcher{match, fn})
}

// AuthorizeKey lets user log in with key. The user "*" stands for any user.
func (s *Server) AuthorizeKey(user string, key ssh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[user] = append(s.keys[user], key)
}

// AllowPassword lets user log in with a password (also as the answer to a
// keyboard-interactive prompt), like a freshly issued VPS's root. An empty
// password turns it off, as sshd's PasswordAuthentication no does.
func (s *Server) AllowPassword(user, password string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if password == "" {
		delete(s.passwords, user)
		return
	}
	s.passwords[user] = password
}

// BlockLogins makes every new login of user ("*": everyone) fail, whatever
// credential it offers; open connections go on. It simulates a server whose
// key login broke.
func (s *Server) BlockLogins(user string, blocked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocked[user] = blocked
}

func (s *Server) keyOK(user string, key ssh.PublicKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked[user] || s.blocked["*"] {
		return false
	}
	for _, u := range []string{user, "*"} {
		for _, k := range s.keys[u] {
			if bytes.Equal(k.Marshal(), key.Marshal()) {
				return true
			}
		}
	}
	return false
}

func (s *Server) passwordOK(user, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want, ok := s.passwords[user]
	return ok && !s.blocked[user] && !s.blocked["*"] && subtle.ConstantTimeCompare([]byte(want), []byte(password)) == 1
}

// SetHostKeyTypes replaces the host keys with fresh ones of these types
// ("ed25519", "ecdsa"). New connections see them; open ones do not change.
func (s *Server) SetHostKeyTypes(types ...string) {
	s.mu.Lock()
	s.hostKeys = nil
	s.mu.Unlock()
	s.AddHostKeyTypes(types...)
}

// AddHostKeyTypes gives the server more host keys beside the ones it has, as a
// server with several key types has.
func (s *Server) AddHostKeyTypes(types ...string) {
	var keys []ssh.Signer
	for _, typ := range types {
		keys = append(keys, newSigner(s.t, typ))
	}
	s.mu.Lock()
	s.hostKeys = append(s.hostKeys, keys...)
	s.mu.Unlock()
}

// newSigner makes a fresh host key of a type ("ed25519", "ecdsa").
func newSigner(t testing.TB, typ string) ssh.Signer {
	var key any
	var err error
	switch typ {
	case "ed25519":
		_, key, err = ed25519.GenerateKey(rand.Reader)
	case "ecdsa":
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	default:
		t.Fatalf("sshxtest: unknown host key type %q", typ)
	}
	if err != nil {
		t.Fatal(err)
	}
	sg, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

// RotateHostKey replaces the host keys with new keys of the same types, as a
// reinstalled server would have.
func (s *Server) RotateHostKey() {
	s.mu.Lock()
	var types []string
	for _, k := range s.hostKeys {
		if k.PublicKey().Type() == ssh.KeyAlgoED25519 {
			types = append(types, "ed25519")
		} else {
			types = append(types, "ecdsa")
		}
	}
	s.mu.Unlock()
	s.SetHostKeyTypes(types...)
}

// HostKey is the server's first host key.
func (s *Server) HostKey() ssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostKeys[0].PublicKey()
}

// Dir is the root of the SFTP file system: absolute paths ("/opt/app/x") and
// relative ones both resolve inside it.
func (s *Server) Dir() string { return s.dir }

// AllowForwarding lets clients open TCP connections through the server, so it
// can be a jump host.
func (s *Server) AllowForwarding() {
	s.mu.Lock()
	s.forwarding = true
	s.mu.Unlock()
}

// Refuse makes the server close every new connection at once, as a host that
// is down (or a closed port) does; open connections go on.
func (s *Server) Refuse(refuse bool) {
	s.mu.Lock()
	s.refusing = refuse
	s.mu.Unlock()
}

// BreakOnGlobalRequest makes a global request (a keepalive) close the
// connection it came on, standing in for RouterOS 7.24.5: it answers one
// with SSH_MSG_UNIMPLEMENTED, which ends the client's packet loop. The client
// sees EOF instead, to the same effect: every command on the connection ends
// without an exit status.
func (s *Server) BreakOnGlobalRequest(on bool) {
	s.mu.Lock()
	s.noGlobal = on
	s.mu.Unlock()
}

// RefuseUploads makes SFTP writes fail with a permission error, as RouterOS
// does for a user whose group lacks the ftp policy.
func (s *Server) RefuseUploads(refuse bool) {
	s.mu.Lock()
	s.noUploads = refuse
	s.mu.Unlock()
}

// OnUpload calls fn as each SFTP write starts, with the path asked for.
func (s *Server) OnUpload(fn func(path string)) {
	s.mu.Lock()
	s.onUpload = fn
	s.mu.Unlock()
}

func (s *Server) uploadStarted(path string) {
	s.mu.Lock()
	fn := s.onUpload
	s.mu.Unlock()
	if fn != nil {
		fn(path)
	}
}

func (s *Server) uploadsRefused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noUploads
}

// Freeze makes the server stop reading and writing all its connections
// without closing them, as a host does that forgot a session or went quiet
// behind a link that stays up: a running command never sends its exit status
// or closes its channel, the client's close and keepalives go unanswered.
// Freeze(false) lets everything held back through.
func (s *Server) Freeze(frozen bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case frozen && s.thaw == nil:
		s.thaw = make(chan struct{})
	case !frozen && s.thaw != nil:
		close(s.thaw)
		s.thaw = nil
	}
}

// gate blocks while the server is frozen, until it thaws or closes.
func (s *Server) gate() {
	s.mu.Lock()
	thaw := s.thaw
	s.mu.Unlock()
	if thaw == nil {
		return
	}
	select {
	case <-thaw:
	case <-s.closed:
	}
}

// gatedConn holds back what was read and what is to be written while the
// server or the connection is frozen.
type gatedConn struct {
	net.Conn
	s      *Server
	mu     sync.Mutex
	frozen bool
}

func (c *gatedConn) freeze() {
	c.mu.Lock()
	c.frozen = true
	c.mu.Unlock()
}

func (c *gatedConn) gate() {
	c.s.gate()
	c.mu.Lock()
	frozen := c.frozen
	c.mu.Unlock()
	if frozen {
		<-c.s.closed
	}
}

func (c *gatedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.gate()
	return n, err
}

func (c *gatedConn) Write(p []byte) (int, error) {
	c.gate()
	return c.Conn.Write(p)
}

// Close stops the server and ends its connections.
func (s *Server) Close() {
	s.mu.Lock()
	for addr, ln := range s.listeners {
		_ = ln.Close()
		delete(s.listeners, addr)
	}
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) accept(ln net.Listener, addr string) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.refusing {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(&gatedConn{Conn: conn, s: s}, addr)
			s.mu.Lock()
			delete(s.conns, conn)
			s.mu.Unlock()
		}()
	}
}

func (s *Server) config(addr string) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if s.keyOK(c.User(), key) {
				return &ssh.Permissions{}, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if s.passwordOK(c.User(), string(pw)) {
				return &ssh.Permissions{}, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
		KeyboardInteractiveCallback: func(c ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := ask(c.User(), "", []string{"Password: "}, []bool{false})
			if err != nil || len(answers) != 1 || !s.passwordOK(c.User(), answers[0]) {
				return nil, io.ErrUnexpectedEOF
			}
			return &ssh.Permissions{}, nil
		},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.hostKeys
	if own, ok := s.ownKeys[addr]; ok {
		keys = own
	}
	for _, k := range keys {
		cfg.AddHostKey(k)
	}
	return cfg
}

func (s *Server) serve(conn *gatedConn, addr string) {
	defer func() { _ = conn.Close() }()
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.config(addr))
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }()
	go s.globalRequests(sc, reqs)
	var wg sync.WaitGroup
	defer wg.Wait()
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			wg.Add(1)
			go func() { defer wg.Done(); s.session(nc, sc.User(), conn.freeze) }()
		case "direct-tcpip":
			s.mu.Lock()
			ok := s.forwarding
			s.mu.Unlock()
			if !ok {
				_ = nc.Reject(ssh.Prohibited, "forwarding is off")
				continue
			}
			wg.Add(1)
			go func() { defer wg.Done(); s.forward(nc) }()
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

func (s *Server) globalRequests(sc *ssh.ServerConn, reqs <-chan *ssh.Request) {
	for req := range reqs {
		s.mu.Lock()
		breaks := s.noGlobal
		s.mu.Unlock()
		if breaks {
			_ = sc.Close()
			go ssh.DiscardRequests(reqs)
			return
		}
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
}

func (s *Server) session(nc ssh.NewChannel, user string, freeze func()) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var p struct{ Command string }
			if ssh.Unmarshal(req.Payload, &p) != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			s.mu.Lock()
			fn := s.handlers[p.Command]
			var scripted func(*Session) int
			if fn == nil {
				for _, m := range s.matchers {
					if m.match(user, p.Command) {
						scripted = m.fn
						break
					}
				}
			}
			s.mu.Unlock()
			code := 127
			switch {
			case fn != nil:
				code = fn(ch, ch, ch.Stderr())
			case scripted != nil:
				code = scripted(&Session{User: user, Command: p.Command, Stdin: ch, Stdout: ch, Stderr: ch.Stderr(), Freeze: freeze})
			default:
				_, _ = io.WriteString(ch.Stderr(), "sshxtest: no such command: "+p.Command+"\n")
			}
			if code != NoExitStatus {
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
			}
			return
		case "subsystem":
			var p struct{ Name string }
			if ssh.Unmarshal(req.Payload, &p) != nil || p.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			srv := sftp.NewRequestServer(ch, (&rootFS{root: s.dir, refuse: s.uploadsRefused, onWrite: s.uploadStarted}).handlers())
			_ = srv.Serve()
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *Server) forward(nc ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigAddr string
		OrigPort uint32
	}
	if ssh.Unmarshal(nc.ExtraData(), &p) != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(target, ch); _ = target.Close() }()
	go func() { defer wg.Done(); _, _ = io.Copy(ch, target); _ = ch.Close() }()
	wg.Wait()
}

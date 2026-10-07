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

// Server is an SSH server on 127.0.0.1.
type Server struct {
	// Addr is "127.0.0.1:port".
	Addr string

	t          testing.TB
	ln         net.Listener
	dir        string
	authorized ssh.PublicKey

	mu         sync.Mutex
	hostKeys   []ssh.Signer
	handlers   map[string]Handler
	forwarding bool
	conns      map[net.Conn]struct{}
	wg         sync.WaitGroup
}

// NewServer starts a server that accepts the authorized key and closes with
// the test. Its host key is a fresh ed25519 key.
func NewServer(t testing.TB, authorized ssh.PublicKey) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr: ln.Addr().String(), t: t, ln: ln, dir: t.TempDir(), authorized: authorized,
		handlers: map[string]Handler{}, conns: map[net.Conn]struct{}{},
	}
	s.SetHostKeyTypes("ed25519")
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.Close)
	return s
}

// Handle scripts a command, matched on its full text.
func (s *Server) Handle(cmd string, fn func(stdin io.Reader, stdout, stderr io.Writer) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[cmd] = fn
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
		switch typ {
		case "ed25519":
			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				s.t.Fatal(err)
			}
			sg, err := ssh.NewSignerFromKey(priv)
			if err != nil {
				s.t.Fatal(err)
			}
			keys = append(keys, sg)
		case "ecdsa":
			priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				s.t.Fatal(err)
			}
			sg, err := ssh.NewSignerFromKey(priv)
			if err != nil {
				s.t.Fatal(err)
			}
			keys = append(keys, sg)
		default:
			s.t.Fatalf("sshxtest: unknown host key type %q", typ)
		}
	}
	s.mu.Lock()
	s.hostKeys = append(s.hostKeys, keys...)
	s.mu.Unlock()
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

// Dir is the SFTP working directory.
func (s *Server) Dir() string { return s.dir }

// AllowForwarding lets clients open TCP connections through the server, so it
// can be a jump host.
func (s *Server) AllowForwarding() {
	s.mu.Lock()
	s.forwarding = true
	s.mu.Unlock()
}

// Close stops the server and ends its connections.
func (s *Server) Close() {
	_ = s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(conn)
			s.mu.Lock()
			delete(s.conns, conn)
			s.mu.Unlock()
		}()
	}
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), s.authorized.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.hostKeys {
		cfg.AddHostKey(k)
	}
	return cfg
}

func (s *Server) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.config())
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	defer wg.Wait()
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			wg.Add(1)
			go func() { defer wg.Done(); s.session(nc) }()
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

func (s *Server) session(nc ssh.NewChannel) {
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
			s.mu.Unlock()
			code := 127
			if fn == nil {
				_, _ = io.WriteString(ch.Stderr(), "sshxtest: no such command: "+p.Command+"\n")
			} else {
				code = fn(ch, ch, ch.Stderr())
			}
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
			return
		case "subsystem":
			var p struct{ Name string }
			if ssh.Unmarshal(req.Payload, &p) != nil || p.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.dir))
			if err != nil {
				return
			}
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

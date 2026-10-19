package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
	"golang.org/x/crypto/ssh"
)

const (
	// OutputCap is how much of each of stdout and stderr Run returns.
	OutputCap = 1 << 20
	// DefaultCommandTimeout bounds a command that sets no timeout.
	DefaultCommandTimeout = 5 * time.Minute
	// DefaultTransferTimeout bounds an upload that sets no timeout.
	DefaultTransferTimeout = 5 * time.Minute
	keepAliveMisses        = 3
)

// Client is an open connection to a target, through its jump host if it has
// one.
type Client struct {
	ssh        *ssh.Client
	jump       *ssh.Client
	log        *jobs.Logger
	closeGrace time.Duration
	// opening, when set, runs before each session opens; tests block in it to
	// stand for an open that never returns.
	opening func()

	stop     chan struct{}
	stopOnce sync.Once
	broke    sync.Once // the reason the connection broke is logged once
}

// Connect opens a connection to t. Failures that no retry can fix
// (*HostKeyChangedError, *UnknownHostError, ErrAuth, tailnet.ErrOff) are
// wrapped in jobs.Permanent; errors.As still finds them.
func (s *SSH) Connect(ctx context.Context, t Target, log *jobs.Logger) (*Client, error) {
	signer, err := s.signer(ctx)
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	var jump *ssh.Client
	if t.Jump != nil {
		if jump, err = s.dialHop(ctx, *t.Jump, t.Network, nil, t.JumpFirstContact, signer, log); err != nil {
			return nil, permanentIfFinal(&HopError{Jump: true, Address: NormalizeAddress(t.Jump.Address), Err: err})
		}
	}
	c, err := s.dialHop(ctx, t.Hop, t.Network, jump, t.FirstContact, signer, log)
	if err != nil {
		if jump != nil {
			_ = jump.Close()
		}
		return nil, permanentIfFinal(&HopError{Address: NormalizeAddress(t.Address), Err: err})
	}
	cl := &Client{ssh: c, jump: jump, log: log, closeGrace: s.CloseGrace, stop: make(chan struct{})}
	go s.keepAlive(cl)
	return cl, nil
}

// HopError says which hop a connection failed at. Its text is the cause's
// own, so messages read as before; errors.As and errors.Is see through it.
type HopError struct {
	Jump    bool   // the jump host; false: the target itself
	Address string // normalised "host:port"
	Err     error
}

func (e *HopError) Error() string { return e.Err.Error() }
func (e *HopError) Unwrap() error { return e.Err }

func permanentIfFinal(err error) error {
	var changed *HostKeyChangedError
	var unknown *UnknownHostError
	if errors.As(err, &changed) || errors.As(err, &unknown) || errors.Is(err, ErrAuth) ||
		errors.Is(err, tailnet.ErrOff) || errors.Is(err, ErrNoIdentity) {
		return jobs.Permanent(err)
	}
	return err
}

// dialHop connects one hop: over via when there is one, otherwise over the
// target's network. It applies the pinning rules.
func (s *SSH) dialHop(ctx context.Context, h Hop, network Network, via *ssh.Client, mode FirstContact, signer ssh.Signer, log *jobs.Logger) (*ssh.Client, error) {
	address := NormalizeAddress(h.Address)
	known, err := s.known(ctx, address)
	if err != nil {
		return nil, err
	}
	// Nothing reconnects until the admin has decided about a changed key.
	if known != nil && known.PendingFingerprint != "" {
		return nil, &HostKeyChangedError{Address: address, Old: known.Fingerprint, New: known.PendingFingerprint,
			TypeChanged: known.PendingKeyType != known.KeyType}
	}

	algos := firstContactAlgorithms
	if known != nil {
		algos = hostKeyAlgorithms(known.KeyType)
	}
	check := &hostCheck{address: address, known: known, mode: mode}
	cfg := &ssh.ClientConfig{
		User: h.User, Auth: authMethods(h, signer),
		HostKeyCallback: check.callback, HostKeyAlgorithms: algos,
	}
	dial := func() (net.Conn, error) { return s.dial(ctx, address, network, via) }

	conn, err := dial()
	if err != nil {
		return nil, err
	}
	client, err := s.handshake(ctx, conn, address, cfg)
	if err != nil {
		return nil, s.explain(ctx, err, check, dial, signer, h.User)
	}

	if known == nil {
		if err := s.Pin(ctx, address, h.Subject, check.key, events.ActorSystem); err != nil {
			_ = client.Close()
			return nil, err
		}
		if log != nil {
			log.Info("Pinned the host key of %s (%s)", address, ssh.FingerprintSHA256(check.key))
		}
	} else {
		s.touch(ctx, address)
	}
	return client, nil
}

// authMethods is the key, or the password when the hop has one.
func authMethods(h Hop, signer ssh.Signer) []ssh.AuthMethod {
	if h.Password == "" {
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}
	}
	pw := h.Password
	return []ssh.AuthMethod{
		ssh.Password(pw),
		ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = pw
			}
			return answers, nil
		}),
	}
}

// explain turns a failed handshake into the error the caller should see,
// recording a changed host key on the way.
func (s *SSH) explain(ctx context.Context, err error, check *hostCheck, dial func() (net.Conn, error), signer ssh.Signer, user string) error {
	switch {
	case check.unknown != nil:
		return check.unknown
	case check.changed:
		old, werr := s.recordChange(ctx, check.address, check.key)
		if werr != nil {
			return errors.Join(werr, err)
		}
		return &HostKeyChangedError{Address: check.address, Old: old, New: fingerprintOf(check.key),
			TypeChanged: check.key.Type() != check.known.KeyType}
	}
	var neg *ssh.AlgorithmNegotiationError
	if check.known != nil && check.key == nil && errors.As(err, &neg) && neg.What == "host key" {
		// The host has no key of the pinned type. Look at what it has.
		if key := s.probe(ctx, dial, signer, user); key != nil {
			old, werr := s.recordChange(ctx, check.address, key)
			if werr != nil {
				return errors.Join(werr, err)
			}
			return &HostKeyChangedError{Address: check.address, Old: old, New: fingerprintOf(key), TypeChanged: true}
		}
	}
	if strings.Contains(err.Error(), "unable to authenticate") {
		return fmt.Errorf("%w: %v", ErrAuth, err)
	}
	return err
}

func fingerprintOf(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

var errProbed = errors.New("sshx: probed")

// probe reads the host key a host offers when every algorithm is allowed. It
// never authenticates.
func (s *SSH) probe(ctx context.Context, dial func() (net.Conn, error), signer ssh.Signer, user string) ssh.PublicKey {
	conn, err := dial()
	if err != nil {
		return nil
	}
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error { got = key; return errProbed },
	}
	if c, err := s.handshake(ctx, conn, "probe", cfg); err == nil {
		_ = c.Close()
	}
	return got
}

// dial opens the TCP connection to address: through via, over the tailnet, or
// directly.
func (s *SSH) dial(ctx context.Context, address string, network Network, via *ssh.Client) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, s.DialTimeout)
	defer cancel()
	switch {
	case via != nil:
		return dialVia(ctx, via, address)
	case network == Tailnet:
		if s.tailnet == nil {
			return nil, tailnet.ErrOff
		}
		return s.tailnet(ctx, "tcp", address)
	}
	if s.Dial != nil {
		return s.Dial(ctx, "tcp", address)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", address)
}

// dialVia dials through an SSH connection, which has no context of its own.
func dialVia(ctx context.Context, via *ssh.Client, address string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := via.Dial("tcp", address)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// handshake runs the SSH handshake and authentication on conn, bounded by the
// handshake timeout and ctx. The connection is closed on failure.
func (s *SSH) handshake(ctx context.Context, conn net.Conn, address string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	_ = conn.SetDeadline(time.Now().Add(s.HandshakeTimeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// keepAlive probes the connection; three misses in a row close it, so a dead
// peer fails the next command at once and not after the TCP timeout. A probe
// that fails at once closes it too: the connection's packet loop is gone (it
// answers io.EOF then), and a session opened on it would wait for good.
func (s *SSH) keepAlive(c *Client) {
	t := time.NewTicker(s.KeepAlive)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
		done := make(chan error, 1)
		go func() { _, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				misses = 0
				continue
			}
			c.drop()
			return
		case <-time.After(s.KeepAlive):
		case <-c.stop:
			return
		}
		if misses++; misses >= keepAliveMisses {
			_ = c.Close()
			return
		}
	}
}

// RunOptions tune one command.
type RunOptions struct {
	Timeout time.Duration // 0: DefaultCommandTimeout
	Stdin   io.Reader
	Log     *jobs.Logger // nil: nothing streamed
}

// Result is what a command printed and how it ended.
type Result struct {
	ExitCode       int
	Stdout, Stderr []byte // each capped at OutputCap, unredacted
}

// Run runs cmd and waits for it. Output goes line by line into o.Log (stderr
// as warnings), which redacts it; the Result keeps it unredacted for the
// caller to parse. A command that exits non-zero is not an error: look at
// ExitCode. A command over its timeout is killed and returns ErrTimeout.
// Run returns within the timeout (or ctx) plus twice CloseGrace: a host that
// never confirms the end of the session gets the whole connection closed, so
// the client is unusable after that. So does a session that ends without an
// exit status: the connection may be broken without knowing it yet.
func (c *Client) Run(ctx context.Context, cmd string, o RunOptions) (Result, error) {
	if c.Closed() {
		return Result{ExitCode: -1}, ErrClosed
	}
	out, errOut := &capWriter{max: OutputCap}, &capWriter{max: OutputCap}
	var stdout, stderr io.Writer = out, errOut
	var closers []io.Closer
	if o.Log != nil {
		lo, le := o.Log.Writer("info"), o.Log.Writer("warn")
		stdout, stderr = io.MultiWriter(out, lo), io.MultiWriter(errOut, le)
		closers = append(closers, lo, le)
	}
	// A session Run gave up on may still write after it returns.
	gOut, gErr := &gate{w: stdout}, &gate{w: stderr}

	// Opening the session, starting the command and waiting for it each wait
	// for the host's answer, so all three run apart from the timeout.
	opened := make(chan *ssh.Session, 1)
	done := make(chan error, 1)
	go func() {
		if c.opening != nil {
			c.opening()
		}
		sess, err := c.ssh.NewSession()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = sess.Close() }()
		sess.Stdout, sess.Stderr, sess.Stdin = gOut, gErr, o.Stdin
		opened <- sess
		if err := sess.Start(cmd); err != nil {
			done <- err
			return
		}
		done <- sess.Wait()
	}()

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr, ending error
	select {
	case waitErr = <-done:
	case <-timer.C:
		ending = ErrTimeout
	case <-ctx.Done():
		ending = ctx.Err()
	}
	if ending != nil {
		c.end(opened, done)
	}
	gOut.shut()
	gErr.shut()
	for _, cl := range closers {
		_ = cl.Close()
	}
	res := Result{ExitCode: -1, Stdout: out.buf, Stderr: errOut.buf}
	if ending != nil {
		return res, ending
	}
	var exit *ssh.ExitError
	switch {
	case waitErr == nil:
		res.ExitCode = 0
	case errors.As(waitErr, &exit):
		res.ExitCode = exit.ExitStatus()
	default:
		if lost(waitErr) {
			c.drop()
		}
		return res, waitErr
	}
	return res, nil
}

// lost reports whether err says a session ended without the host ending it.
// The connection's packet loop may be gone without the transport knowing yet:
// behind a jump host its socket is a channel that ends only when the jump host
// answers the close, and a session opened in the meantime is never answered
// (a router once broke the loop with its answer to a keepalive).
func lost(err error) bool {
	var missing *ssh.ExitMissingError
	return errors.As(err, &missing) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, sftp.ErrSSHFxConnectionLost)
}

// drop closes a connection whose session was lost and logs why the
// connection broke, when it broke on its own.
func (c *Client) drop() {
	_ = c.Close()
	why := make(chan error, 1)
	go func() { why <- c.ssh.Wait() }()
	select {
	case err := <-why:
		if err != nil && !lost(err) && c.log != nil {
			c.broke.Do(func() { c.log.Warn("The connection broke: %v.", err) })
		}
	case <-time.After(c.closeGrace):
	}
}

// gate passes writes on until it is shut.
type gate struct {
	mu     sync.Mutex
	w      io.Writer
	closed bool
}

func (g *gate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return len(p), nil
	}
	return g.w.Write(p)
}

func (g *gate) shut() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// end ends a command Run gave up on. It closes the session and waits
// CloseGrace for the host to confirm; a host that never does (RouterOS can
// drop a command's channel and keep the connection), or a session still
// opening, costs the whole connection, and CloseGrace more for the session to
// go. A session opened on a connection whose packet loop is gone outlives the
// close (nothing tracks it), so after that it is left behind. The kill and the
// close are writes, which a dead link blocks for good, so they don't hold up
// the wait.
func (c *Client) end(opened <-chan *ssh.Session, done <-chan error) {
	select {
	case sess := <-opened:
		go func() {
			_ = sess.Signal(ssh.SIGKILL) // not every server honours it; closing the session ends the rest
			_ = sess.Close()
		}()
		select {
		case <-done:
			return
		case <-time.After(c.closeGrace):
		}
	default:
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(c.closeGrace):
	}
}

// TransferOptions tune one upload.
type TransferOptions struct {
	Timeout time.Duration // 0: DefaultTransferTimeout
}

// transfer runs fn on a new SFTP client, bounded like Run: once the timeout
// or ctx ends it, the SFTP client is closed and given CloseGrace, then the
// whole connection is closed and given CloseGrace more. Starting SFTP waits
// for the host's answer too, so it runs inside the bound. A lost SFTP session
// costs the connection, as a lost command does.
func (c *Client) transfer(ctx context.Context, timeout time.Duration, fn func(*sftp.Client) error) error {
	if c.Closed() {
		return ErrClosed
	}
	var mu sync.Mutex
	var sc *sftp.Client
	done := make(chan error, 1)
	go func() {
		if c.opening != nil {
			c.opening()
		}
		s, err := sftp.NewClient(c.ssh)
		if err != nil {
			done <- fmt.Errorf("sshx: start SFTP: %w", err)
			return
		}
		mu.Lock()
		sc = s
		mu.Unlock()
		err = fn(s)
		_ = s.Close()
		done <- err
	}()

	if timeout <= 0 {
		timeout = DefaultTransferTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var ending error
	select {
	case err := <-done:
		if err != nil && lost(err) {
			c.drop()
		}
		return err
	case <-timer.C:
		ending = ErrTimeout
	case <-ctx.Done():
		ending = ctx.Err()
	}
	mu.Lock()
	s := sc
	mu.Unlock()
	if s != nil {
		go func() { _ = s.Close() }() // a write too: a dead link blocks it
		select {
		case <-done:
			return ending
		case <-time.After(c.closeGrace):
		}
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(c.closeGrace):
	}
	return ending
}

// Upload writes data to path over SFTP: to <path>.proxier-tmp first, then
// renamed over the target, so a reader never sees half a file. Parent
// directories are not created. An upload over its timeout returns ErrTimeout
// and may have cost the connection, as a command does.
func (c *Client) Upload(ctx context.Context, path string, data []byte, mode fs.FileMode, o TransferOptions) error {
	return c.transfer(ctx, o.Timeout, func(sc *sftp.Client) error {
		tmp := path + ".proxier-tmp"
		f, err := sc.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return fmt.Errorf("sshx: create %s: %w", tmp, err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			_ = sc.Remove(tmp)
			return fmt.Errorf("sshx: write %s: %w", tmp, err)
		}
		if err := sc.Chmod(tmp, mode); err != nil {
			_ = f.Close()
			_ = sc.Remove(tmp)
			return fmt.Errorf("sshx: chmod %s: %w", tmp, err)
		}
		_ = f.Sync() // fsync@openssh.com where the server has it; best effort
		if err := f.Close(); err != nil {
			_ = sc.Remove(tmp)
			return fmt.Errorf("sshx: close %s: %w", tmp, err)
		}
		if err := sc.PosixRename(tmp, path); err != nil {
			// A server without posix-rename: plain SFTP rename refuses to replace,
			// so the target is removed first. Not atomic, but the data is whole.
			_ = sc.Remove(path)
			if err := sc.Rename(tmp, path); err != nil {
				_ = sc.Remove(tmp)
				return fmt.Errorf("sshx: rename %s: %w", tmp, err)
			}
		}
		return nil
	})
}

// Put writes data to path over SFTP in place: no temporary file, chmod, fsync
// or rename, which RouterOS's minimal SFTP server may lack. A reader can see
// half a file, so it is for files nothing reads until the caller says so
// (a script /import runs afterwards). Bounded as Upload is.
func (c *Client) Put(ctx context.Context, path string, data []byte, o TransferOptions) error {
	return c.transfer(ctx, o.Timeout, func(sc *sftp.Client) error {
		f, err := sc.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return fmt.Errorf("sshx: create %s: %w", path, err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("sshx: write %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("sshx: close %s: %w", path, err)
		}
		return nil
	})
}

// Closed reports whether the connection was closed: by Close, by the
// keepalive, by Run giving up on a session the host never ended, or by a
// session that was lost.
func (c *Client) Closed() bool {
	select {
	case <-c.stop:
		return true
	default:
		return false
	}
}

// Close ends the connection, and the jump host's. The jump host's goes
// first: closing the target's connection writes through it, which a dead
// link blocks for good, while closing the jump host's only closes a socket
// and fails every channel through it.
func (c *Client) Close() error {
	var err error
	c.stopOnce.Do(func() {
		close(c.stop)
		if c.jump == nil {
			err = c.ssh.Close()
			return
		}
		err = c.jump.Close()
		_ = c.ssh.Close() // fails at once on the closed jump connection
	})
	return err
}

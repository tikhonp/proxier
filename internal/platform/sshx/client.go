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
	keepAliveMisses       = 3
)

// Client is an open connection to a target, through its jump host if it has
// one.
type Client struct {
	ssh  *ssh.Client
	jump *ssh.Client
	log  *jobs.Logger

	stop     chan struct{}
	stopOnce sync.Once
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
		if jump, err = s.dialHop(ctx, *t.Jump, t.Network, nil, t.FirstContact, signer, log); err != nil {
			return nil, permanentIfFinal(err)
		}
	}
	c, err := s.dialHop(ctx, t.Hop, t.Network, jump, t.FirstContact, signer, log)
	if err != nil {
		if jump != nil {
			_ = jump.Close()
		}
		return nil, permanentIfFinal(err)
	}
	cl := &Client{ssh: c, jump: jump, log: log, stop: make(chan struct{})}
	go s.keepAlive(cl)
	return cl, nil
}

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
// peer fails the next command at once and not after the TCP timeout.
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
func (c *Client) Run(ctx context.Context, cmd string, o RunOptions) (Result, error) {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	defer func() { _ = sess.Close() }()

	out, errOut := &capWriter{max: OutputCap}, &capWriter{max: OutputCap}
	var stdout, stderr io.Writer = out, errOut
	var closers []io.Closer
	if o.Log != nil {
		lo, le := o.Log.Writer("info"), o.Log.Writer("warn")
		stdout, stderr = io.MultiWriter(out, lo), io.MultiWriter(errOut, le)
		closers = append(closers, lo, le)
	}
	sess.Stdout, sess.Stderr, sess.Stdin = stdout, stderr, o.Stdin

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	if err := sess.Start(cmd); err != nil {
		return Result{ExitCode: -1}, err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()

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
		_ = sess.Signal(ssh.SIGKILL) // not every server honours it; closing the session ends the rest
		_ = sess.Close()
		<-done // the output copiers end with the session; nothing writes after this
	}
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
		return res, waitErr
	}
	return res, nil
}

// Upload writes data to path over SFTP: to <path>.proxier-tmp first, then
// renamed over the target, so a reader never sees half a file. Parent
// directories are not created.
func (c *Client) Upload(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	sc, err := sftp.NewClient(c.ssh)
	if err != nil {
		return fmt.Errorf("sshx: start SFTP: %w", err)
	}
	defer func() { _ = sc.Close() }()
	// The SFTP client has no context; closing it on cancel stops the transfer.
	stop := context.AfterFunc(ctx, func() { _ = sc.Close() })
	defer stop()

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
}

// Close ends the connection, and the jump host's.
func (c *Client) Close() error {
	var err error
	c.stopOnce.Do(func() {
		close(c.stop)
		err = c.ssh.Close()
		if c.jump != nil {
			_ = c.jump.Close()
		}
	})
	return err
}

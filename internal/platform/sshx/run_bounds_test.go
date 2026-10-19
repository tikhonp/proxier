package sshx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"github.com/tikhonp/proxier/internal/platform/testwait"
	"golang.org/x/crypto/ssh"
)

// These cover a host that stops answering a command's session: it never sends
// the exit status or closes the channel, and ignores Proxier's close (a
// router's /import once hung a sync like this). Run must still return within
// its timeout plus CloseGrace, and close the connection it can no longer
// trust.

const (
	boundTimeout = 100 * time.Millisecond
	boundGrace   = 100 * time.Millisecond
)

// runWithin runs fn and fails the test if it has not returned within limit.
func runWithin(t *testing.T, limit time.Duration, fn func() (Result, error)) (Result, time.Duration, error) {
	t.Helper()
	type ret struct {
		res Result
		err error
	}
	ch := make(chan ret, 1)
	start := time.Now()
	go func() { r, err := fn(); ch <- ret{r, err} }()
	select {
	case r := <-ch:
		return r.res, time.Since(start), r.err
	case <-time.After(testwait.For(limit)):
		t.Fatalf("Run did not return within %s", testwait.For(limit))
		return Result{}, 0, nil
	}
}

// silentAfterStart scripts cmd to accept the exec and then freeze the server.
func silentAfterStart(srv *sshxtest.Server, cmd string) {
	srv.Handle(cmd, func(io.Reader, io.Writer, io.Writer) int {
		srv.Freeze(true)
		return 0
	})
}

func (e *env) boundedSSH() {
	e.ssh.CloseGrace = boundGrace
	e.ssh.KeepAlive = time.Hour // the keepalive must not be what rescues Run
}

func assertConnectionClosed(t *testing.T, c *Client) {
	t.Helper()
	_, _, err := runWithin(t, 2*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "echo", RunOptions{Timeout: time.Minute})
	})
	if err == nil {
		t.Fatal("the connection Run gave up on still runs commands")
	}
}

func TestRunTimesOutWhenTheHostNeverEndsTheSession(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	srv := e.server()
	silentAfterStart(srv, "/import file-name=x.rsc")
	c := e.mustConnect(e.target(srv, "router:1"))

	res, took, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/import file-name=x.rsc", RunOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) || res.ExitCode != -1 {
		t.Fatalf("want ErrTimeout with an unknown exit code, got %+v, %v", res, err)
	}
	if took < boundTimeout {
		t.Fatalf("Run returned after %s, before its timeout", took)
	}
	assertConnectionClosed(t, c)
}

func TestRunTimesOutWhenTheHostStopsAnsweringBeforeTheSessionOpens(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	srv := e.server()
	c := e.mustConnect(e.target(srv, "router:1"))
	srv.Freeze(true)

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/import file-name=x.rsc", RunOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	assertConnectionClosed(t, c)
}

func TestRunReturnsOnCancelWhenTheHostNeverEndsTheSession(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	srv := e.server()
	silentAfterStart(srv, "/import file-name=x.rsc")
	c := e.mustConnect(e.target(srv, "router:1"))

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(boundTimeout, cancel)
	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(ctx, "/import file-name=x.rsc", RunOptions{Timeout: time.Hour})
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	assertConnectionClosed(t, c)
}

func TestRunTimesOutWhenTheHostBehindAJumpHostNeverEndsTheSession(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	jump, target := e.server(), e.server()
	jump.AllowForwarding()
	silentAfterStart(target, "/import file-name=x.rsc")
	c := e.mustConnect(Target{
		Hop:  Hop{Address: target.Addr, User: "proxier", Subject: "router:2"},
		Jump: &Hop{Address: jump.Addr, User: "pi", Subject: "jump:pi"},
	})

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/import file-name=x.rsc", RunOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	assertConnectionClosed(t, c)
}

func TestRunTimesOutWhenTheJumpHostStopsForwarding(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	jump, target := e.server(), e.server()
	jump.AllowForwarding()
	c := e.mustConnect(Target{
		Hop:  Hop{Address: target.Addr, User: "proxier", Subject: "router:2"},
		Jump: &Hop{Address: jump.Addr, User: "pi", Subject: "jump:pi"},
	})
	jump.Freeze(true)

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/import file-name=x.rsc", RunOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	assertConnectionClosed(t, c)
}

// A jump host whose link dies without a word: nothing more arrives and every
// write through it blocks, as a tailnet peer that vanished. Whatever was
// waiting must still end at its bound, and the close must not wait on the
// dead link (a sync through a Pi once hung for good like this, its import
// done and its session gone from the Pi).

// viaDyingJump connects to target through a jump host; kill makes the link
// to the jump host die silently.
func (e *env) viaDyingJump(target *sshxtest.Server) (c *Client, kill func()) {
	e.t.Helper()
	jump := e.server()
	jump.AllowForwarding()
	links := sshxtest.NewLinks()
	e.ssh.Dial = links.Dial
	c = e.mustConnect(Target{
		Hop:  Hop{Address: target.Addr, User: "proxier", Subject: "router:2"},
		Jump: &Hop{Address: jump.Addr, User: "pi", Subject: "jump:pi"},
	})
	return c, func() { links.Stall(jump.Addr) }
}

// longCommand scripts cmd to run until the test ends; started is closed once
// it runs.
func (e *env) longCommand(srv *sshxtest.Server, cmd string) (started <-chan struct{}) {
	ch, release := make(chan struct{}), make(chan struct{})
	e.t.Cleanup(func() { close(release) }) // before the server's own cleanup closes it
	srv.Handle(cmd, func(io.Reader, io.Writer, io.Writer) int {
		close(ch)
		<-release
		return 0
	})
	return ch
}

func TestRunTimesOutWhenTheJumpLinkDiesSilently(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	target := e.server()
	started := e.longCommand(target, "/import file-name=x.rsc")
	c, kill := e.viaDyingJump(target)
	go func() { <-started; kill() }()

	_, took, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/import file-name=x.rsc", RunOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if took < boundTimeout {
		t.Fatalf("Run returned after %s, before its timeout", took)
	}
	assertConnectionClosed(t, c)
}

func TestRunReturnsOnCancelWhenTheJumpLinkDiesSilently(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	target := e.server()
	started := e.longCommand(target, "/import file-name=x.rsc")
	c, kill := e.viaDyingJump(target)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; kill(); time.Sleep(boundTimeout); cancel() }()

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(ctx, "/import file-name=x.rsc", RunOptions{Timeout: time.Hour})
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	assertConnectionClosed(t, c)
}

func TestKeepAliveEndsACommandWhoseJumpLinkDiedSilently(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	e.ssh.KeepAlive = 20 * time.Millisecond
	target := e.server()
	started := e.longCommand(target, "/import file-name=x.rsc")
	c, kill := e.viaDyingJump(target)
	go func() { <-started; kill() }()

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/import file-name=x.rsc", RunOptions{Timeout: time.Hour})
	})
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("the keepalive ends the command on the dead link, got %v", err)
	}
	if !c.Closed() {
		t.Fatal("the keepalive closed the connection")
	}
}

func TestCloseReturnsWhenTheJumpLinkDiedSilently(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	c, kill := e.viaDyingJump(e.server())
	kill()

	_, _, _ = runWithin(t, 2*time.Second, func() (Result, error) { return Result{}, c.Close() })
	assertConnectionClosed(t, c)
}

func TestPutReturnsOnCancelWhenTheJumpLinkDiesSilently(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	c, kill := e.viaDyingJump(e.server())
	kill()
	ctx, cancel := context.WithTimeout(context.Background(), boundTimeout)
	defer cancel()

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return Result{}, c.Put(ctx, "proxier-sync-1-1.rsc", []byte("x\n"), TransferOptions{Timeout: time.Hour})
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context's error, got %v", err)
	}
	assertConnectionClosed(t, c)
}

func TestPutTimesOutWhenTheJumpLinkDiesSilently(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	c, kill := e.viaDyingJump(e.server())
	kill()

	_, took, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return Result{}, c.Put(context.Background(), "proxier-sync-1-1.rsc", []byte("x\n"), TransferOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if took < boundTimeout {
		t.Fatalf("Put returned after %s, before its timeout", took)
	}
	assertConnectionClosed(t, c)
}

func TestUploadTimesOutWhenTheJumpLinkDiesMidTransfer(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	target := e.server()
	c, kill := e.viaDyingJump(target)
	// SFTP is running when the link dies: the upload is past its start.
	if err := c.Upload(context.Background(), filepath.Join(target.Dir(), "before"), []byte("x"), 0o600, TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	kill()

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return Result{}, c.Upload(context.Background(), filepath.Join(target.Dir(), "f"), make([]byte, 1<<20), 0o600, TransferOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	assertConnectionClosed(t, c)
}

func TestPutWithinItsTimeoutUploads(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	srv := e.server()
	c := e.mustConnect(e.target(srv, "router:1"))
	if err := c.Put(context.Background(), "x.rsc", []byte("x\n"), TransferOptions{Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(srv.Dir(), "x.rsc")); err != nil || string(b) != "x\n" {
		t.Fatalf("uploaded %q, %v", b, err)
	}
	if c.Closed() {
		t.Fatal("an upload in time keeps the connection")
	}
}

// RouterOS 7.24.5 answers the 30 s keepalive in a way that ends the client's
// packet loop. Behind a jump host the connection doesn't know it yet: its
// socket is a channel that ends only when the jump host answers the close. A
// sync once opened its next session in that window and waited for good.

// neverOpens makes every session and SFTP client of c wait to open until the
// test ends, as one opened on a connection whose packet loop is gone: closing
// the connection doesn't end it.
func neverOpens(t *testing.T, c *Client) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	c.opening = func() { <-release }
}

func TestRunTimesOutWhenTheSessionNeverOpensAndClosingCannotEndIt(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	c := e.mustConnect(e.target(e.server(), "router:1"))
	neverOpens(t, c)

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/file/remove x.rsc", RunOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if !c.Closed() {
		t.Fatal("the connection Run gave up on is closed")
	}
}

func TestPutTimesOutWhenSFTPNeverStartsAndClosingCannotEndIt(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	c := e.mustConnect(e.target(e.server(), "router:1"))
	neverOpens(t, c)

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return Result{}, c.Put(context.Background(), "x.rsc", []byte("x\n"), TransferOptions{Timeout: boundTimeout})
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if !c.Closed() {
		t.Fatal("the connection Put gave up on is closed")
	}
}

func TestASessionLostBehindAJumpHostClosesTheConnection(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	jump, target := e.server(), e.server()
	jump.AllowForwarding()
	target.Handle("/import file-name=x.rsc", func(io.Reader, io.Writer, io.Writer) int { return sshxtest.NoExitStatus })
	c := e.mustConnect(Target{
		Hop:  Hop{Address: target.Addr, User: "proxier", Subject: "router:2"},
		Jump: &Hop{Address: jump.Addr, User: "pi", Subject: "jump:pi"},
	})

	_, _, err := runWithin(t, 3*time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/import file-name=x.rsc", RunOptions{Timeout: time.Minute})
	})
	var missing *ssh.ExitMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("want the missing exit status, got %v", err)
	}
	if !c.Closed() {
		t.Fatal("a lost session closes the connection")
	}
	// No session is opened on it again: the next command fails at once.
	_, _, err = runWithin(t, time.Second, func() (Result, error) {
		return c.Run(context.Background(), "/file/remove x.rsc", RunOptions{Timeout: time.Minute})
	})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
	if err := c.Put(context.Background(), "x.rsc", []byte("x\n"), TransferOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed from Put, got %v", err)
	}
}

func TestACommandWithAnExitStatusKeepsTheConnection(t *testing.T) {
	e := newEnv(t)
	e.boundedSSH()
	srv := e.server()
	srv.Handle("false", func(io.Reader, io.Writer, io.Writer) int { return 1 })
	c := e.mustConnect(e.target(srv, "router:1"))

	res, err := c.Run(context.Background(), "false", RunOptions{Timeout: time.Minute})
	if err != nil || res.ExitCode != 1 {
		t.Fatalf("want exit code 1, got %+v, %v", res, err)
	}
	if c.Closed() {
		t.Fatal("a command that failed on its own keeps the connection")
	}
}

func TestAKeepAliveThatFailsClosesTheConnectionAtOnce(t *testing.T) {
	e := newEnv(t)
	e.ssh.KeepAlive = 300 * time.Millisecond
	srv := e.server()
	c := e.mustConnect(e.target(srv, "router:1"))
	srv.Close()

	// Three misses would take 600 ms past the first failed probe.
	deadline := time.Now().Add(500 * time.Millisecond)
	for !c.Closed() {
		if time.Now().After(deadline) {
			t.Fatal("the first failed probe closes the connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

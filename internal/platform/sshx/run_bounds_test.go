package sshx

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"github.com/tikhonp/proxier/internal/platform/testwait"
)

// These cover a host that stops answering a command's session: it never sends
// the exit status or closes the channel, and ignores Proxier's close (router
// sync job #3837 hung like this on an /import). Run must still return within
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

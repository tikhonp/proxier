package sshx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
)

func TestClientRestrictsHostKeyAlgorithmToPinned(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	tg := e.target(srv, "server:1")
	e.mustConnect(tg)
	pinned := fp(srv.HostKey())

	// The server now also has an ECDSA key. A client that took whatever the
	// server prefers could be handed that one and see a "change".
	srv.AddHostKeyTypes("ecdsa")
	e.mustConnect(tg)
	if len(e.events("ssh.host_key_changed")) != 0 || e.hosts()[0].Fingerprint != pinned {
		t.Fatal("a second key type looked like a changed key")
	}

	// A server with only another type fails as a changed key type.
	srv.SetHostKeyTypes("ecdsa")
	_, err := e.connect(tg)
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) || !changed.TypeChanged || changed.Old != pinned || changed.New != fp(srv.HostKey()) {
		t.Fatalf("want a key type change, got %v", err)
	}
	if !strings.Contains(err.Error(), "key type changed") {
		t.Fatalf("the message must say so: %v", err)
	}
	if ev := e.events("ssh.host_key_changed"); len(ev) != 1 {
		t.Fatalf("events: %+v", ev)
	}
	if h := e.hosts()[0]; h.PendingFingerprint != fp(srv.HostKey()) || h.Fingerprint != pinned {
		t.Fatalf("known host: %+v", h)
	}
	// The admin decides as for any change.
	if err := e.ssh.Accept(context.Background(), e.hosts()[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	e.mustConnect(tg)
}

func TestJumpHostPinsBothHops(t *testing.T) {
	e := newEnv(t)
	jump, target := e.server(), e.server()
	jump.AllowForwarding()
	target.Handle("hostname", func(_ io.Reader, out, _ io.Writer) int { _, _ = io.WriteString(out, "the-router\n"); return 0 })

	tg := Target{
		Hop:  Hop{Address: target.Addr, User: "admin", Subject: "router:3"},
		Jump: &Hop{Address: jump.Addr, User: "jumper", Subject: "jump:parents-pi"},
	}
	c := e.mustConnect(tg)
	res, err := c.Run(context.Background(), "hostname", RunOptions{})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "the-router\n" {
		t.Fatalf("run through the jump host: %+v, %v", res, err)
	}
	bySubject := map[string]string{}
	for _, h := range e.hosts() {
		bySubject[h.Subject] = h.Fingerprint
	}
	if bySubject["router:3"] != fp(target.HostKey()) || bySubject["jump:parents-pi"] != fp(jump.HostKey()) || len(bySubject) != 2 {
		t.Fatalf("both hops must be pinned under their own subjects: %v", bySubject)
	}
}

func TestJumpHostKeyChangeStops(t *testing.T) {
	e := newEnv(t)
	jump, target := e.server(), e.server()
	jump.AllowForwarding()
	tg := Target{Hop: Hop{Address: target.Addr, User: "admin", Subject: "router:3"}, Jump: &Hop{Address: jump.Addr, User: "jumper", Subject: "jump:pi"}}
	e.mustConnect(tg)

	jump.RotateHostKey()
	_, err := e.connect(tg)
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) || changed.Address != jump.Addr {
		t.Fatalf("a changed jump host key must stop the connection, got %v", err)
	}
	if len(e.events("ssh.host_key_changed")) != 1 {
		t.Fatal("the change was not recorded")
	}
}

func TestRefusedKey(t *testing.T) {
	e := newEnv(t)
	other := newEnv(t) // a server that trusts a different Proxier
	srv := sshxtest.NewServer(t, other.pub)

	_, err := e.connect(e.target(srv, "server:1"))
	if !errors.Is(err, ErrAuth) || !jobs.IsPermanent(err) {
		t.Fatalf("want a permanent ErrAuth, got %v", err)
	}
	if len(e.hosts()) != 0 {
		t.Fatal("a host that refused the key was pinned")
	}
}

func TestCommandTimeout(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv.Handle("sleep", func(io.Reader, io.Writer, io.Writer) int { <-release; return 0 })
	srv.Handle("echo", func(_ io.Reader, out, _ io.Writer) int { _, _ = io.WriteString(out, "ok"); return 0 })
	c := e.mustConnect(e.target(srv, "server:1"))

	start := time.Now()
	res, err := c.Run(context.Background(), "sleep", RunOptions{Timeout: 50 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) || res.ExitCode != -1 {
		t.Fatalf("want ErrTimeout with an unknown exit code, got %+v, %v", res, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the timeout did not end the command")
	}
	// The connection is fine; only the session was closed.
	if res, err := c.Run(context.Background(), "echo", RunOptions{}); err != nil || string(res.Stdout) != "ok" {
		t.Fatalf("the next command: %+v, %v", res, err)
	}
}

func TestRunStreamsRedactedOutput(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	srv.Handle("deploy", func(_ io.Reader, out, errOut io.Writer) int {
		_, _ = io.WriteString(out, "token=hunter2\nplain line\nlast line without newline hunter2")
		_, _ = io.WriteString(errOut, "warning: hunter2 leaked\n")
		return 0
	})
	tg := e.target(srv, "server:1")

	var stdout, stderr []byte
	err := e.h.Sys.Register("platform", jobs.Type{
		Name: "platform.sshtest", Queue: jobs.Maintenance, MaxAttempts: 1,
		Steps: []jobs.Step{{Name: "run", Run: func(ctx context.Context, r *jobs.Run) error {
			r.Log().Redact("hunter2")
			c, err := e.ssh.Connect(ctx, tg, r.Log())
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			res, err := c.Run(ctx, "deploy", RunOptions{Log: r.Log()})
			stdout, stderr = res.Stdout, res.Stderr
			return err
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.h.Start(e.h.Sys)
	enq, err := e.h.Sys.EnqueueNow(context.Background(), jobs.Request{Type: "platform.sshtest", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	e.h.Drain()
	if st := e.h.State(enq.ID); st != jobs.Succeeded {
		t.Fatalf("job state %s", st)
	}

	// The step gets the real output, to parse it.
	if !bytes.Contains(stdout, []byte("hunter2")) || !bytes.Contains(stderr, []byte("hunter2")) {
		t.Fatalf("the step must get unredacted output: %q %q", stdout, stderr)
	}
	lines, err := e.h.Sys.LogTail(context.Background(), enq.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{} // text → level
	for _, l := range lines {
		if strings.Contains(l.Text, "hunter2") {
			t.Fatalf("a secret reached the log: %q", l.Text)
		}
		got[l.Text] = l.Level
	}
	for text, level := range map[string]string{
		"token=•••": "info", "plain line": "info", "last line without newline •••": "info", "warning: ••• leaked": "warn",
	} {
		if got[text] != level {
			t.Errorf("log line %q: level %q, want %q (log: %v)", text, got[text], level, got)
		}
	}
}

func TestRunReturnsExitCode(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	srv.Handle("fail", func(_ io.Reader, _, errOut io.Writer) int { _, _ = io.WriteString(errOut, "boom\n"); return 3 })
	srv.Handle("cat", func(in io.Reader, out, _ io.Writer) int { _, _ = io.Copy(out, in); return 0 })
	c := e.mustConnect(e.target(srv, "server:1"))

	res, err := c.Run(context.Background(), "fail", RunOptions{})
	if err != nil || res.ExitCode != 3 || string(res.Stderr) != "boom\n" {
		t.Fatalf("a failing command: %+v, %v", res, err)
	}
	res, err = c.Run(context.Background(), "nonsense", RunOptions{})
	if err != nil || res.ExitCode != 127 {
		t.Fatalf("an unknown command: %+v, %v", res, err)
	}
	res, err = c.Run(context.Background(), "cat", RunOptions{Stdin: strings.NewReader("from stdin")})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "from stdin" {
		t.Fatalf("stdin: %+v, %v", res, err)
	}
}

func TestOutputIsCapped(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	srv.Handle("big", func(_ io.Reader, out, _ io.Writer) int {
		_, _ = out.Write(bytes.Repeat([]byte("x"), OutputCap+4096))
		return 0
	})
	c := e.mustConnect(e.target(srv, "server:1"))
	res, err := c.Run(context.Background(), "big", RunOptions{})
	if err != nil || len(res.Stdout) != OutputCap {
		t.Fatalf("stdout %d bytes, %v", len(res.Stdout), err)
	}
}

func TestUploadReplacesAtomically(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	c := e.mustConnect(e.target(srv, "server:1"))
	path := filepath.Join(srv.Dir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Upload(context.Background(), path, []byte("new content"), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new content" {
		t.Fatalf("content %q, %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if _, err := os.Stat(path + ".proxier-tmp"); !os.IsNotExist(err) {
		t.Fatal("the temporary file was left behind")
	}

	// A new file works too, and parent directories are not created.
	if err := c.Upload(context.Background(), filepath.Join(srv.Dir(), "fresh"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = c.Upload(context.Background(), filepath.Join(srv.Dir(), "missing", "dir", "f"), []byte("x"), 0o600)
	if err == nil {
		t.Fatal("a missing parent directory must fail")
	}
}

func TestTailnetTargetWhileOff(t *testing.T) {
	e := newEnv(t)
	off := tailnet.New(&config.Config{DataDir: t.TempDir()}, dbtest.Discard)
	ssh := New(e.h.DB, e.h.Vault, e.h.Events, e.h.Settings, off.Dial, dbtest.Discard)
	srv := e.server()
	tg := e.target(srv, "router:1")
	tg.Network = Tailnet

	_, err := ssh.Connect(context.Background(), tg, nil)
	if !errors.Is(err, tailnet.ErrOff) || !jobs.IsPermanent(err) {
		t.Fatalf("want a permanent tailnet.ErrOff, got %v", err)
	}
	// No dialer at all behaves the same.
	if _, err := e.ssh.Connect(context.Background(), tg, nil); !errors.Is(err, tailnet.ErrOff) {
		t.Fatalf("no tailnet dialer: %v", err)
	}
}

func TestKeepAliveClosesADeadConnection(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	e.ssh.KeepAlive = 20 * time.Millisecond
	c := e.mustConnect(e.target(srv, "server:1"))
	// A peer that stops answering: the server goes away without closing.
	srv.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Run(context.Background(), "x", RunOptions{Timeout: 100 * time.Millisecond}); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a dead connection kept working")
}

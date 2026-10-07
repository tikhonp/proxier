package sshx

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"golang.org/x/crypto/ssh"
)

// env is an SSH client on a migrated database with the jobs harness around it.
type env struct {
	t   *testing.T
	h   *jobstest.Harness
	ssh *SSH
	pub ssh.PublicKey // Proxier's own key
}

func newEnv(t *testing.T) *env {
	t.Helper()
	h := jobstest.New(t)
	if err := h.Events.Declare(Events...); err != nil {
		t.Fatal(err)
	}
	if err := h.Settings.Register(Section); err != nil {
		t.Fatal(err)
	}
	s := New(h.DB, h.Vault, h.Events, h.Settings, nil, dbtest.Discard)
	if err := s.EnsureIdentity(context.Background(), "Proxier"); err != nil {
		t.Fatal(err)
	}
	line, _, err := s.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, h: h, ssh: s, pub: pub}
}

// server starts a test server that accepts Proxier's key.
func (e *env) server() *sshxtest.Server { return sshxtest.NewServer(e.t, e.pub) }

func (e *env) target(srv *sshxtest.Server, subject string) Target {
	return Target{Hop: Hop{Address: srv.Addr, User: "root", Subject: subject}}
}

// events returns the recorded events of a type, newest first.
func (e *env) events(typ string) []events.Event {
	e.t.Helper()
	l, err := events.List(context.Background(), e.h.DB.R, events.Filter{Type: typ, Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func (e *env) connect(t Target) (*Client, error) {
	return e.ssh.Connect(context.Background(), t, nil)
}

func (e *env) mustConnect(t Target) *Client {
	e.t.Helper()
	c, err := e.connect(t)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(func() { _ = c.Close() })
	return c
}

func (e *env) hosts() []KnownHost {
	e.t.Helper()
	h, err := e.ssh.KnownHosts(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func fp(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

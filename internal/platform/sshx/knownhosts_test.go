package sshx

import (
	"context"
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func TestFirstContactPinsHostKey(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	tg := e.target(srv, "server:12")

	e.mustConnect(tg)
	hosts := e.hosts()
	if len(hosts) != 1 || hosts[0].Address != srv.Addr || hosts[0].Subject != "server:12" ||
		hosts[0].Fingerprint != fp(srv.HostKey()) || hosts[0].KeyType != "ssh-ed25519" || hosts[0].PendingFingerprint != "" {
		t.Fatalf("known hosts: %+v", hosts)
	}
	ev := e.events("ssh.host_key_pinned")
	if len(ev) != 1 || ev[0].Payload["address"] != srv.Addr || ev[0].Payload["fingerprint"] != fp(srv.HostKey()) {
		t.Fatalf("events: %+v", ev)
	}

	// The second contact compares and records nothing.
	e.h.Clock.Advance(1)
	e.mustConnect(tg)
	if len(e.events("ssh.host_key_pinned")) != 1 || len(e.hosts()) != 1 {
		t.Fatal("a known host was pinned again")
	}
}

func TestFirstContactNeedsConfirmation(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	tg := e.target(srv, "router:3")
	tg.FirstContact = ConfirmFirstContact

	_, err := e.connect(tg)
	var unknown *UnknownHostError
	if !errors.As(err, &unknown) || unknown.Fingerprint != fp(srv.HostKey()) || unknown.Address != srv.Addr {
		t.Fatalf("want an UnknownHostError with the fingerprint, got %v", err)
	}
	if len(e.hosts()) != 0 || len(e.events("ssh.host_key_pinned")) != 0 {
		t.Fatal("something was pinned before the admin confirmed")
	}

	if err := e.ssh.Pin(context.Background(), srv.Addr, "router:3", unknown.Key, "admin"); err != nil {
		t.Fatal(err)
	}
	e.mustConnect(tg)
	ev := e.events("ssh.host_key_pinned")
	if len(ev) != 1 || ev[0].Actor != "admin" {
		t.Fatalf("events: %+v", ev)
	}
	// Pinning the same key again is not a change.
	if err := e.ssh.Pin(context.Background(), srv.Addr, "router:3", unknown.Key, "admin"); err != nil || len(e.events("ssh.host_key_pinned")) != 1 {
		t.Fatalf("pin twice: %v", err)
	}
}

func TestChangedHostKeyStopsEveryConnection(t *testing.T) {
	e := newEnv(t)
	srv := e.server()
	tg := e.target(srv, "server:1")
	e.mustConnect(tg)
	old := fp(srv.HostKey())

	srv.RotateHostKey()
	for i := range 3 {
		_, err := e.connect(tg)
		var changed *HostKeyChangedError
		if !errors.As(err, &changed) || changed.Old != old || changed.New != fp(srv.HostKey()) || changed.Address != srv.Addr {
			t.Fatalf("attempt %d: want HostKeyChangedError, got %v", i, err)
		}
		if changed.TypeChanged {
			t.Fatal("same type, new key: TypeChanged must be false")
		}
		if !jobs.IsPermanent(err) {
			t.Fatalf("attempt %d: the error must be permanent for jobs", i)
		}
	}
	ev := e.events("ssh.host_key_changed")
	if len(ev) != 1 || ev[0].Payload["old"] != old || ev[0].Payload["new"] != fp(srv.HostKey()) {
		t.Fatalf("one change, one event: %+v", ev)
	}
	hosts := e.hosts()
	if len(hosts) != 1 || hosts[0].PendingFingerprint != fp(srv.HostKey()) || hosts[0].Fingerprint != old {
		t.Fatalf("the pending key must wait beside the pinned one: %+v", hosts)
	}
}

func TestAcceptNewKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	srv := e.server()
	tg := e.target(srv, "server:1")
	e.mustConnect(tg)
	old := fp(srv.HostKey())
	srv.RotateHostKey()
	if _, err := e.connect(tg); err == nil {
		t.Fatal("a changed key connected")
	}
	id := e.hosts()[0].ID

	if err := e.ssh.Accept(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	e.mustConnect(tg)
	h := e.hosts()[0]
	if h.Fingerprint != fp(srv.HostKey()) || h.PendingFingerprint != "" {
		t.Fatalf("after accept: %+v", h)
	}
	ev := e.events("ssh.host_key_accepted")
	if len(ev) != 1 || ev[0].Payload["old"] != old || ev[0].Payload["new"] != fp(srv.HostKey()) || ev[0].Actor != "admin" {
		t.Fatalf("events: %+v", ev)
	}
	if err := e.ssh.Accept(ctx, id, "admin"); !errors.Is(err, ErrNoPending) {
		t.Fatalf("accepting nothing: %v", err)
	}
	if err := e.ssh.Accept(ctx, 9999, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestForgetHost(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	srv := e.server()
	tg := e.target(srv, "server:1")
	e.mustConnect(tg)
	srv.RotateHostKey()

	if err := e.ssh.Forget(ctx, e.hosts()[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if len(e.hosts()) != 0 {
		t.Fatal("the host is still pinned")
	}
	ev := e.events("ssh.host_forgotten")
	if len(ev) != 1 || ev[0].Payload["address"] != srv.Addr {
		t.Fatalf("events: %+v", ev)
	}
	// The next contact is a first contact: the new key is simply pinned.
	e.mustConnect(tg)
	if h := e.hosts(); len(h) != 1 || h[0].Fingerprint != fp(srv.HostKey()) {
		t.Fatalf("known hosts: %+v", h)
	}
	if err := e.ssh.Forget(ctx, 9999, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestForgetSubject(t *testing.T) {
	e := newEnv(t)
	a, b, other := e.server(), e.server(), e.server()
	e.mustConnect(e.target(a, "server:1"))
	e.mustConnect(e.target(b, "server:1"))
	e.mustConnect(e.target(other, "server:2"))

	if err := e.ssh.ForgetSubject(context.Background(), "server:1", "system"); err != nil {
		t.Fatal(err)
	}
	h := e.hosts()
	if len(h) != 1 || h[0].Address != other.Addr {
		t.Fatalf("known hosts: %+v", h)
	}
	if ev := e.events("ssh.host_forgotten"); len(ev) != 2 {
		t.Fatalf("one event per host, got %d", len(ev))
	}
	if err := e.ssh.ForgetSubject(context.Background(), "server:99", "system"); err != nil || len(e.events("ssh.host_forgotten")) != 2 {
		t.Fatal("forgetting nothing must record nothing")
	}
}

func TestAddressesAreNormalized(t *testing.T) {
	for in, want := range map[string]string{
		"Example.COM:22": "example.com:22", "example.com": "example.com:22", "[::1]:2222": "[::1]:2222", "[::1]": "[::1]:22",
	} {
		if got := NormalizeAddress(in); got != want {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

package routers_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"golang.org/x/crypto/ssh"
)

// runTest starts a test and waits for it to end or stop at a fingerprint.
func runTest(t *testing.T, h *routingtest.Harness, routerID int64, c routers.Connection) routers.Test {
	t.Helper()
	h.StartJobs()
	id, err := h.Mod.Routers.StartTest(bg, routerID, c, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Settle()
	return mustTest(t, h, id)
}

func mustTest(t *testing.T, h *routingtest.Harness, id int64) routers.Test {
	t.Helper()
	tt, err := h.Mod.Routers.Test(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func confirm(t *testing.T, h *routingtest.Harness, id int64) routers.Test {
	t.Helper()
	next, err := h.Mod.Routers.Confirm(bg, id, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Settle()
	return mustTest(t, h, next)
}

func knownHosts(t *testing.T, h *routingtest.Harness) map[string]sshx.KnownHost {
	t.Helper()
	hosts, err := h.App.SSH.KnownHosts(bg)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]sshx.KnownHost{}
	for _, k := range hosts {
		out[k.Address] = k
	}
	return out
}

func fakes(t *testing.T, h *routingtest.Harness, jump bool) (*routerostest.Router, *sshxtest.Server, routers.Connection) {
	t.Helper()
	r := routerostest.New(t, h.Key())
	var j *sshxtest.Server
	if jump {
		j = routerostest.Jump(t, h.Key())
	}
	return r, j, routingtest.Conn(r, j)
}

func TestAddRouterBehindJumpHost(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, jump, c := fakes(t, h, true)

	first := runTest(t, h, 0, c)
	if first.State != routers.TestConfirm || first.ConfirmHop != "jump" || first.ConfirmAddr != c.JumpAddress() ||
		first.ConfirmFP != ssh.FingerprintSHA256(jump.HostKey()) {
		t.Fatalf("the jump host first: %+v", first)
	}
	if len(knownHosts(t, h)) != 0 {
		t.Fatal("nothing pinned before the admin confirms")
	}
	second := confirm(t, h, first.ID)
	if second.State != routers.TestConfirm || second.ConfirmHop != "router" || second.ConfirmFP != ssh.FingerprintSHA256(r.Server.HostKey()) {
		t.Fatalf("then the router: %+v", second)
	}
	third := confirm(t, h, second.ID)
	if third.State != routers.TestPassed || third.Version != "7.24.5 (stable)" || third.Board != "RB5009UG+S+" {
		t.Fatalf("passed: %+v", third)
	}
	for _, ch := range third.Checks {
		if !ch.OK || ch.Warn {
			t.Fatalf("no warning: %+v", third.Checks)
		}
	}
	if len(third.Checks) != 4 || third.Checks[2].Detail != "2" {
		t.Fatalf("checks: %+v", third.Checks)
	}
	if hosts := knownHosts(t, h); hosts[c.JumpAddress()].Subject != routers.JumpSubject(c.JumpAddress()) || hosts[c.Address()].Subject != "router:new" {
		t.Fatalf("pinned: %+v", hosts)
	}

	id, err := h.Mod.Routers.Create(bg, "Parents", mainList(t, h), c, third.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if hosts := knownHosts(t, h); hosts[c.Address()].Subject != routers.RouterSubject(id) {
		t.Fatalf("the router's pin is its own now: %+v", hosts[c.Address()])
	}
	if ev := h.Events("routing.router_added"); len(ev) != 1 || ev[0].Payload["list"] != "Main" || ev[0].Payload["by"] != "admin" {
		t.Fatalf("added: %+v", ev)
	}
	if ev := h.Events("routing.router_connected"); len(ev) != 1 || ev[0].Payload["version"] != "7.24.5 (stable)" {
		t.Fatalf("connected: %+v", ev)
	}
	h.Settle()
	if s := lastSync(t, h, id); s.State != "done" || s.Trigger != "initial" || s.Added != 1 {
		t.Fatalf("initial sync: %+v", s)
	}
	if !r.Holds("openai.com") {
		t.Fatal("synced through the jump host")
	}
}

func TestDeclineFingerprintPinsNothing(t *testing.T) {
	h := routingtest.New(t)
	_, _, c := fakes(t, h, true)
	first := runTest(t, h, 0, c)
	if first.State != routers.TestConfirm {
		t.Fatalf("test: %+v", first)
	}
	if n, err := h.Mod.Routers.Confirm(bg, first.ID, false, "admin"); err != nil || n != 0 {
		t.Fatalf("stop: %d %v", n, err)
	}
	got := mustTest(t, h, first.ID)
	if got.State != routers.TestFailed || got.Error != routers.Stopped {
		t.Fatalf("stopped: %+v", got)
	}
	if len(knownHosts(t, h)) != 0 || len(h.Events("ssh.host_key_pinned")) != 0 {
		t.Fatal("nothing pinned")
	}
	if _, err := h.Mod.Routers.Confirm(bg, first.ID, true, "admin"); err == nil {
		t.Fatal("a stopped test can't be confirmed")
	}
}

func TestMissingForwarderWarns(t *testing.T) {
	h := routingtest.New(t)
	r, _, c := fakes(t, h, false)
	r.NoForwarder()
	got := confirm(t, h, runTest(t, h, 0, c).ID)
	if got.State != routers.TestWarned {
		t.Fatalf("warned: %+v", got)
	}
	var warn routers.CheckResult
	for _, ch := range got.Checks {
		if ch.Warn {
			warn = ch
		}
	}
	if warn.Name != routers.CheckForwarder || warn.Detail != "This router doesn't look set up by the router script: no DoH forwarder vpn-doh." {
		t.Fatalf("warning: %+v", got.Checks)
	}
	id, err := h.Mod.Routers.Create(bg, "Home", mainList(t, h), c, got.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if rt := router(t, h, id); !rt.Connected() {
		t.Fatal("Save is still allowed and connects")
	}
}

func TestSaveUntestedWaitsForTest(t *testing.T) {
	h := routingtest.New(t)
	main := mainList(t, h)
	_, _, c := fakes(t, h, false)
	id, err := h.Mod.Routers.Create(bg, "Home", main, c, 0, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// a test of other values doesn't count either
	other := c
	other.Names.List = "other_list"
	tested := runTest(t, h, 0, other)
	id2, err := h.Mod.Routers.Create(bg, "Home 2", main, c, tested.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, rid := range []int64{id, id2} {
		if router(t, h, rid).Connected() {
			t.Fatal("saved untested: not connected")
		}
	}
	if len(h.Events("routing.router_connected")) != 0 || len(h.JobsOf(routers.JobSync)) != 0 || len(h.Events("routing.router_added")) != 2 {
		t.Fatal("no connection, no initial sync")
	}
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	if len(h.JobsOf(routers.JobSync)) != 0 {
		t.Fatal("a change queues nothing for it")
	}
	// its first passing test connects it and queues the initial sync
	first := runTest(t, h, id, c)
	if first.State != routers.TestConfirm || first.ConfirmHop != "router" {
		t.Fatalf("first contact: %+v", first)
	}
	passed := confirm(t, h, first.ID)
	if passed.State != routers.TestPassed || passed.RouterID != id {
		t.Fatalf("passed: %+v", passed)
	}
	if ev := h.Events("routing.router_connected"); len(ev) != 1 || ev[0].Subject != routers.Subject(id) {
		t.Fatalf("connected: %+v", ev)
	}
	if hosts := knownHosts(t, h); hosts[c.Address()].Subject != routers.RouterSubject(id) {
		t.Fatalf("pinned for the router: %+v", hosts)
	}
	h.Settle()
	if s := lastSync(t, h, id); s.Trigger != "initial" || s.State != "done" {
		t.Fatalf("initial sync: %+v", s)
	}
	if rt := router(t, h, id); rt.Version == "" || rt.LastSeenAt.IsZero() {
		t.Fatalf("the test updated the router: %+v", rt)
	}
	// Sync now on the other one, never tested, is allowed and fails with the first-contact message
	h.Exec(`DELETE FROM known_hosts`)
	syncNow(t, h, id2)
	if s := lastSync(t, h, id2); s.State != "failed" || !strings.HasPrefix(s.Error, "First contact with "+c.Address()+": confirm its fingerprint with Test connection.") {
		t.Fatalf("first contact: %+v", s)
	}
}

func TestUploadCheck(t *testing.T) {
	h := routingtest.New(t)
	r, _, c := fakes(t, h, false)
	r.NoFTP()
	got := confirm(t, h, runTest(t, h, 0, c).ID)
	if got.State != routers.TestFailed {
		t.Fatalf("failed: %+v", got)
	}
	last := got.Checks[len(got.Checks)-1]
	if last.Name != routers.CheckUpload || last.OK || !strings.Contains(last.Detail, "ftp policy") {
		t.Fatalf("upload check: %+v", got.Checks)
	}
	if len(r.Files()) != 0 {
		t.Fatal("nothing left behind")
	}
}

package ipchange_test

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare/cloudflaretest"
	"github.com/tikhonp/proxier/internal/modules/servers/ipchange"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

var bg = context.Background()

const newIP = "10.77.0.5"

// fixture is an active nl-1 at 127.0.0.1 on the fake VPS, which answers at
// every address, so a new IP is the same machine unless a test routes it
// elsewhere.
type fixture struct {
	*serverstest.Harness
	ID int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	if s := h.Server(id); s.State != "active" {
		t.Fatalf("not active: %s", s.FailedError)
	}
	return &fixture{Harness: h, ID: id}
}

// change runs Change IP to the end and returns its job.
func (f *fixture) change(form ipchange.Form) jobs.Job {
	f.T.Helper()
	id, err := f.Mod.IPChange.Change(bg, f.ID, form, "member:1")
	if err != nil {
		f.T.Fatalf("change: %v", err)
	}
	f.Drain()
	j, err := f.App.Jobs.Job(bg, id)
	if err != nil {
		f.T.Fatal(err)
	}
	return j
}

// record is what the fake Cloudflare holds for the server's hostname.
func (f *fixture) record() string {
	f.T.Helper()
	name := f.Server(f.ID).ManagementHostname
	for _, r := range f.CF.Records("proxier.test") {
		if r.Name == name {
			return r.Content
		}
	}
	return ""
}

func (f *fixture) previous() []store.PreviousIP {
	f.T.Helper()
	p, err := store.PreviousIPs(bg, f.App.DB.R, f.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	return p
}

func (f *fixture) pinned(ip string) bool {
	f.T.Helper()
	_, err := f.App.SSH.KnownHost(bg, net.JoinHostPort(ip, strconv.Itoa(f.Server(f.ID).SSHPort)))
	if err != nil && !errors.Is(err, sshx.ErrNotFound) {
		f.T.Fatal(err)
	}
	return err == nil
}

// untouched says the server is still at 127.0.0.1, in Proxier and in DNS.
func (f *fixture) untouched() {
	f.T.Helper()
	s := f.Server(f.ID)
	recs, err := store.DNSRecords(bg, f.App.DB.R, f.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	if s.IP != "127.0.0.1" || f.record() != "127.0.0.1" || len(recs) != 1 || recs[0].Content != "127.0.0.1" ||
		len(f.previous()) != 0 || len(f.Events("server.ip_changed")) != 0 {
		f.T.Fatalf("the server changed: ip %s, cloudflare %s, records %+v, previous %+v", s.IP, f.record(), recs, f.previous())
	}
}

func TestChangeMovesTheServerAndKeepsTheOldIP(t *testing.T) {
	f := newFixture(t)
	f.BlockIP("127.0.0.1", true) // blocked: why the IP changes
	if _, err := f.App.DB.W.Exec(`UPDATE servers_servers SET health = 'blocked' WHERE id = ?`, f.ID); err != nil {
		t.Fatal(err)
	}
	eps, _ := store.Endpoints(bg, f.App.DB.R, f.ID)

	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s\n%s", j.State, j.Error, f.Log(j.ID))
	}
	s := f.Server(f.ID)
	recs, _ := store.DNSRecords(bg, f.App.DB.R, f.ID)
	if s.IP != newIP || f.record() != newIP || len(recs) != 1 || recs[0].Content != newIP || s.IPChangedAt.IsZero() {
		t.Fatalf("ip %s, cloudflare %s, records %+v", s.IP, f.record(), recs)
	}
	if p := f.previous(); len(p) != 1 || p[0].IP != "127.0.0.1" || p[0].State != store.PreviousFails {
		t.Fatalf("previous IPs: %+v", p) // tested once after the change: blocked
	}
	ev := f.Events("server.ip_changed")
	if len(ev) != 1 || ev[0].Payload["from"] != "127.0.0.1" || ev[0].Payload["to"] != newIP || ev[0].Payload["kept"] != true ||
		ev[0].Actor != "job:"+strconv.FormatInt(j.ID, 10) {
		t.Fatalf("events: %+v", ev)
	}
	if !f.pinned(newIP) || !f.pinned("127.0.0.1") {
		t.Fatal("both addresses should be pinned: the new one checked, the old one kept")
	}
	// Clients follow the hostname: the endpoints are the same.
	after, _ := store.Endpoints(bg, f.App.DB.R, f.ID)
	if len(after) != len(eps) || after[0].Host != eps[0].Host || after[0].Host != s.ProxyHostname {
		t.Fatalf("endpoints changed: %+v → %+v", eps, after)
	}
	// Health started over from the new IP's checks, which pass.
	if s.Health != "healthy" {
		t.Fatalf("health %s: %s", s.Health, s.HealthReason)
	}
	hc := f.Events("server.health_changed")
	if len(hc) != 2 || hc[1].Payload["from"] != "blocked" || hc[1].Payload["to"] != "unknown" ||
		hc[1].Payload["reason_key"] != "health.reason.ip_changed" || hc[0].Payload["to"] != "healthy" {
		t.Fatalf("health changes: %+v", hc)
	}
}

func TestChangeToAnotherMachineChangesNothing(t *testing.T) {
	f := newFixture(t)
	other := serverstest.NewVPS(t)
	f.RouteSSH(newIP, other.Addr)

	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Failed || j.ErrorStep != ipchange.StepConnect || !strings.Contains(j.Error, "another machine") {
		t.Fatalf("job %s at %s: %s", j.State, j.ErrorStep, j.Error)
	}
	f.untouched()
	if f.pinned(newIP) {
		t.Fatal("the other machine's key was pinned")
	}
	ev := f.Events("server.ip_change_failed")
	if len(ev) != 1 || ev[0].Payload["to"] != newIP || ev[0].Payload["step"] != ipchange.StepConnect {
		t.Fatalf("events: %+v", ev)
	}
}

func TestChangeToAnIPWithoutSSHChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.Unreachable(newIP, true)
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Failed || j.ErrorStep != ipchange.StepConnect || !strings.Contains(j.Error, "is the IP set up on the VPS") {
		t.Fatalf("job %s at %s: %s", j.State, j.ErrorStep, j.Error)
	}
	f.untouched()
}

func TestChangeToABlockedIPChangesNothingUnlessForced(t *testing.T) {
	f := newFixture(t)
	f.BlockIP(newIP, true)
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Failed || j.ErrorStep != ipchange.StepProxyTest || !strings.Contains(j.Error, "nothing was changed") {
		t.Fatalf("job %s at %s: %s", j.State, j.ErrorStep, j.Error)
	}
	f.untouched()

	j = f.change(ipchange.Form{IP: newIP, Keep: true, Force: true})
	if j.State != jobs.Succeeded || !strings.Contains(f.Log(j.ID), "Switching anyway") {
		t.Fatalf("forced job %s: %s", j.State, j.Error)
	}
	if s := f.Server(f.ID); s.IP != newIP {
		t.Fatalf("ip %s", s.IP)
	}
	if ev := f.Events("server.ip_changed"); len(ev) != 1 || ev[0].Payload["forced"] != true {
		t.Fatalf("events: %+v", ev)
	}
}

func TestChangeWithoutKeepingForgetsTheOldIP(t *testing.T) {
	f := newFixture(t)
	j := f.change(ipchange.Form{IP: newIP})
	if j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s", j.State, j.Error)
	}
	if len(f.previous()) != 0 {
		t.Fatalf("previous IPs: %+v", f.previous())
	}
	if f.pinned("127.0.0.1") {
		t.Fatal("the old address is still pinned: a server built there later would see a changed key")
	}
	if ev := f.Events("server.ip_changed"); len(ev) != 1 || ev[0].Payload["kept"] != false {
		t.Fatalf("events: %+v", ev)
	}
}

func TestSwitchBackToAPreviousIP(t *testing.T) {
	f := newFixture(t)
	if j := f.change(ipchange.Form{IP: newIP, Keep: true}); j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s", j.State, j.Error)
	}
	j := f.change(ipchange.Form{IP: "127.0.0.1", Keep: true})
	if j.State != jobs.Succeeded {
		t.Fatalf("back: %s: %s", j.State, j.Error)
	}
	if s := f.Server(f.ID); s.IP != "127.0.0.1" || f.record() != "127.0.0.1" {
		t.Fatalf("ip %s, cloudflare %s", s.IP, f.record())
	}
	if p := f.previous(); len(p) != 1 || p[0].IP != newIP {
		t.Fatalf("previous IPs: %+v", p)
	}
	if ev := f.Events("server.ip_changed"); len(ev) != 2 || ev[0].Payload["back"] != true {
		t.Fatalf("events: %+v", ev)
	}
}

func TestChangeFormRefusals(t *testing.T) {
	f := newFixture(t)
	otherID := f.AddServer("10.77.0.2")
	if j := f.change(ipchange.Form{IP: newIP, Keep: true}); j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s", j.State, j.Error)
	}
	for _, c := range []struct {
		server  int64
		ip, key string
	}{
		{f.ID, "", "servers.err.ip_required"},
		{f.ID, "2001:db8::1", "servers.err.ip_v4"},
		{f.ID, "nl-1", "servers.err.ip_v4"},
		{f.ID, newIP, "servers.ip.err.same"},
		{f.ID, "10.77.0.2", "servers.err.ip_used"},
		{otherID, "127.0.0.1", "servers.err.ip_previous"}, // nl-1 keeps it
	} {
		_, err := f.Mod.IPChange.Change(bg, c.server, ipchange.Form{IP: c.ip, Keep: true}, "member:1")
		var fe ipchange.FieldErrors
		if !errors.As(err, &fe) || fe["ip"].Key != c.key {
			t.Errorf("%q: got %v, want %s", c.ip, err, c.key)
		}
	}
	if n := len(f.Events("server.ip_changed")); n != 1 {
		t.Fatalf("%d changes", n)
	}
}

func TestConflictingDNSRecordChangesNothing(t *testing.T) {
	f := newFixture(t)
	name := f.Server(f.ID).ManagementHostname
	for _, r := range f.CF.Records("proxier.test") {
		if r.Name == name {
			f.CF.Change(r.ID, func(r *cloudflaretest.Record) { r.Comment = "by hand" })
		}
	}
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Failed || j.ErrorStep != ipchange.StepDNS || !strings.Contains(j.Error, "Proxier did not create") {
		t.Fatalf("job %s at %s: %s", j.State, j.ErrorStep, j.Error)
	}
	if s := f.Server(f.ID); s.IP != "127.0.0.1" || f.record() != "127.0.0.1" {
		t.Fatalf("ip %s, cloudflare %s", s.IP, f.record())
	}
}

func TestForgetAPreviousIP(t *testing.T) {
	f := newFixture(t)
	if j := f.change(ipchange.Form{IP: newIP, Keep: true}); j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s", j.State, j.Error)
	}
	p := f.previous()
	if err := f.Mod.IPChange.Forget(bg, f.ID, p[0].ID, "member:1"); err != nil {
		t.Fatal(err)
	}
	if len(f.previous()) != 0 || f.pinned("127.0.0.1") {
		t.Fatal("the previous IP or its pin is still there")
	}
	var n int
	if err := f.App.DB.R.Get(&n, `SELECT count(*) FROM servers_previous_ip_results`); err != nil || n != 0 {
		t.Fatalf("%d results left: %v", n, err)
	}
	if ev := f.Events("server.previous_ip_forgotten"); len(ev) != 1 || ev[0].Payload["ip"] != "127.0.0.1" {
		t.Fatalf("events: %+v", ev)
	}
	if err := f.Mod.IPChange.Forget(bg, f.ID, p[0].ID, "member:1"); !errors.Is(err, ipchange.ErrNotFound) {
		t.Fatalf("forget twice: %v", err)
	}
	// The IP is free: a new server can be built there.
	f.AddServer("127.0.0.1")
}

func TestStackThatNamesTheIPAsksForARedeploy(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	h.PublishVersion(func(files map[string][]byte) {
		files["site/index.html"] = append(files["site/index.html"], []byte("<!-- {{ .Server.IP }} -->")...)
	}, true)
	f := &fixture{Harness: h, ID: h.Provisioned()}
	if names, err := h.Mod.Deploy.StackNamesIP(bg, f.ID, newIP); err != nil || !names {
		t.Fatalf("names the IP: %v %v", names, err)
	}
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Succeeded || !strings.Contains(f.Log(j.ID), "redeploy nl-1 to bring them to "+newIP) {
		t.Fatalf("job %s: %s\n%s", j.State, j.Error, f.Log(j.ID))
	}
	// The seed template names the hostname only.
	g := newFixture(t)
	if names, err := g.Mod.Deploy.StackNamesIP(bg, g.ID, newIP); err != nil || names {
		t.Fatalf("the seed names the IP: %v %v", names, err)
	}
}

// The wait asks the zone's nameservers for the new IP before any resolver,
// and still ends when port 53 to them is dropped.
func TestWaitAsksTheNameserversFirst(t *testing.T) {
	f := newFixture(t)
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s", j.State, j.Error)
	}
	log := f.Log(j.ID)
	ns := strings.Index(log, "proxier.test's nameservers (ns.fake) answer "+newIP)
	cf := strings.Index(log, "Cloudflare (doh) answered "+newIP)
	if ns < 0 || cf < ns {
		t.Fatalf("the nameservers should be asked first:\n%s", log)
	}

	f.NameserversDown(true)
	j = f.change(ipchange.Form{IP: "127.0.0.1", Keep: true})
	if j.State != jobs.Succeeded || f.Server(f.ID).IP != "127.0.0.1" {
		t.Fatalf("job %s: %s", j.State, j.Error)
	}
	if log := f.Log(j.ID); !strings.Contains(log, "Can't ask ns.fake (127.0.0.1) about nl-1.hosts.proxier.test: i/o timeout; asking the public resolvers in 1ms instead") ||
		!strings.Contains(log, "Cloudflare (doh) answered 127.0.0.1") {
		t.Fatalf("log:\n%s", log)
	}
}

// A record made by hand: the member points it at the new IP, and the change
// checks it before it switches.
func TestChangeWithARecordMadeByHand(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	h.SetRecord("vpn.example.org", "127.0.0.1")
	form := h.Form()
	form.Hostname = "vpn.example.org"
	f := &fixture{Harness: h, ID: h.Create(form)}
	h.Drain()
	if s := h.Server(f.ID); s.State != "active" || !s.ManualDNS {
		t.Fatalf("server %+v", s)
	}

	// Not pointed yet: nothing changes.
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Failed || !strings.Contains(j.Error, "A vpn.example.org → "+newIP) {
		t.Fatalf("job %s: %s", j.State, j.Error)
	}
	if s := h.Server(f.ID); s.IP != "127.0.0.1" || len(f.previous()) != 0 || len(h.Events("server.ip_changed")) != 0 {
		t.Fatalf("the server changed: %+v", s)
	}

	h.SetRecord("vpn.example.org", newIP)
	j = f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s\n%s", j.State, j.Error, h.Log(j.ID))
	}
	recs, _ := store.DNSRecords(bg, h.App.DB.R, f.ID)
	if s := h.Server(f.ID); s.IP != newIP || len(recs) != 0 || len(h.CF.Records("proxier.test")) != 0 {
		t.Fatalf("ip %s, records %+v", s.IP, recs)
	}
	if p := f.previous(); len(p) != 1 || p[0].IP != "127.0.0.1" {
		t.Fatalf("previous IPs: %+v", p)
	}
}

func TestChangeToAnAdditionalAddressIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := f.Mod.Deploy.AddAddress(bg, f.ID, "10.77.0.9", "alt.example.org", "member:1"); err != nil {
		t.Fatalf("add address: %v", err)
	}
	_, err := f.Mod.IPChange.Check(bg, f.Server(f.ID), ipchange.Form{IP: "10.77.0.9", Keep: true})
	var fe ipchange.FieldErrors
	if !errors.As(err, &fe) || fe["ip"].Key != "servers.ip.err.additional" {
		t.Fatalf("errors: %+v", err)
	}
}

package ipchange_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/ipchange"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// withAltEndpoint gives the server an additional address and an endpoint on
// it, which the stack rejects (as a server whose alt listener isn't up yet).
func (f *fixture) withAltEndpoint() {
	f.T.Helper()
	const host = "alt.example.org"
	if err := f.Mod.Deploy.AddAddress(bg, f.ID, "10.77.0.9", host, "member:1"); err != nil {
		f.T.Fatalf("add address: %v", err)
	}
	rows, err := store.Endpoints(bg, f.App.DB.R, f.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	eps, err := sealed.OpenEndpoints(f.App.Vault, rows)
	if err != nil {
		f.T.Fatal(err)
	}
	e := eps[0]
	e.Key, e.Host, e.DisplayName = "alt", host, "alt"
	e.Params = map[string]string{"path": "/alt", "sni": host}
	eps = append(eps, e)
	sr := sealed.SealEndpoints(f.App.Vault, f.ID, eps)
	for i := range sr {
		sr[i].Position = i
	}
	if err := f.App.DB.Write(bg, func(tx *sqlx.Tx) error {
		return store.ReplaceEndpoints(bg, tx, f.ID, sr, db.At(time.Now()))
	}); err != nil {
		f.T.Fatal(err)
	}
	f.ProxyAccepts(func(e endpoint.Endpoint) bool { return e.Host != host })
}

// The proxy test of a change covers the endpoints on the primary address
// only: one on an additional address stays where it is, and aimed at the new
// IP it would fail for no reason of the change's.
func TestChangeSkipsEndpointsOnAdditionalAddresses(t *testing.T) {
	f := newFixture(t)
	f.withAltEndpoint()
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s\n%s", j.State, j.Error, f.Log(j.ID))
	}
	log := f.Log(j.ID)
	if !strings.Contains(log, "Skipped alt") || strings.Contains(log, "through alt at") || strings.Contains(log, "Switching anyway") {
		t.Fatalf("log:\n%s", log)
	}
	if s := f.Server(f.ID); s.IP != newIP {
		t.Fatalf("ip %s", s.IP)
	}
}

// A primary endpoint that fails still stops the change, whatever else the
// server has.
func TestChangeWithAdditionalAddressStillTestsThePrimary(t *testing.T) {
	f := newFixture(t)
	f.withAltEndpoint()
	f.BlockIP(newIP, true)
	if j := f.change(ipchange.Form{IP: newIP, Keep: true}); j.State != jobs.Failed || j.ErrorStep != ipchange.StepProxyTest {
		t.Fatalf("job %s at %s: %s", j.State, j.ErrorStep, j.Error)
	}
	f.untouched()
}

// newSSHIPFixture is an active server with an explicit SSH IP; when that is
// not the IP, the IP never answers SSH.
func newSSHIPFixture(t *testing.T, management string) *fixture {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	form := h.Form()
	form.SSHIP = management
	if management != form.IP {
		h.Unreachable(form.IP, true)
	}
	id := h.Create(form)
	h.Drain()
	if s := h.Server(id); s.State != "active" {
		t.Fatalf("not active: %s", s.FailedError)
	}
	// The form stores an SSH IP equal to the IP as empty. A server gets there
	// only through an IP change onto its SSH connection IP, so set it here.
	if management == form.IP {
		if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET ssh_ip = ? WHERE id = ?`, management, id); err != nil {
			t.Fatal(err)
		}
	}
	return &fixture{Harness: h, ID: id}
}

// With an explicit SSH connection IP the change needs SSH there only: the new
// IP may refuse SSH, and the SSH connection IP stays pinned even when it is
// the old IP and the old IP is not kept.
func TestChangeKeepsSSHThroughTheSSHIP(t *testing.T) {
	for _, mgmt := range []string{"100.64.0.10", "127.0.0.1"} {
		t.Run(mgmt, func(t *testing.T) {
			f := newSSHIPFixture(t, mgmt)
			f.Unreachable(newIP, true)
			j := f.change(ipchange.Form{IP: newIP})
			if j.State != jobs.Succeeded {
				t.Fatalf("job %s: %s\n%s", j.State, j.Error, f.Log(j.ID))
			}
			s := f.Server(f.ID)
			if s.IP != newIP || s.SSHIP != mgmt || f.record() != newIP {
				t.Fatalf("ip %s, ssh ip %s, cloudflare %s", s.IP, s.SSHIP, f.record())
			}
			if !f.pinned(mgmt) {
				t.Fatal("the SSH connection IP's host key was forgotten")
			}
			if f.pinned(newIP) {
				t.Fatal("the new IP was pinned without SSH to it")
			}
		})
	}
}

// Without the SSH connection IP answering, nothing changes.
func TestChangeWithSSHIPDownChangesNothing(t *testing.T) {
	f := newSSHIPFixture(t, "100.64.0.10")
	f.Unreachable("100.64.0.10", true)
	j := f.change(ipchange.Form{IP: newIP, Keep: true})
	if j.State != jobs.Failed || j.ErrorStep != ipchange.StepConnect || !strings.Contains(j.Error, "SSH connection IP") {
		t.Fatalf("job %s at %s: %s", j.State, j.ErrorStep, j.Error)
	}
	f.untouched()
}

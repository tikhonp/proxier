package provision_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// An address that belongs to another server is refused with that server's
// name, on the new-server form and on the Addresses page's service alike
// (docs/processes/servers/server-provisioning.md#checks).
func TestAddressOfAnotherServerIsRefusedByName(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	other := h.AddServer("10.77.0.2")
	otherName := h.Server(other).Name
	if err := h.Mod.Deploy.AddAddress(bg, other, "127.0.0.9", "other-alt.example.org", "admin"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, ip, host, field, key, holder string
	}{
		{"its primary IP", "10.77.0.2", "x.example.org", "ip", "servers.err.addr_ip_used", otherName},
		{"its additional IP", "127.0.0.9", "x.example.org", "ip", "servers.err.addr_ip_used", otherName},
		{"its additional hostname", "127.0.0.10", "other-alt.example.org", "hostname", "servers.err.addr_host_used", otherName},
	} {
		t.Run("page "+c.name, func(t *testing.T) {
			err := h.Mod.Deploy.AddAddress(bg, id, c.ip, c.host, "admin")
			m := refused(t, err)[c.field]
			if m.Key != c.key || m.Args["name"] != c.holder {
				t.Fatalf("%+v, want %s naming %q", m, c.key, c.holder)
			}
		})
		t.Run("form "+c.name, func(t *testing.T) {
			f := h.Form()
			f.IP = "127.0.0.20"
			f.Addresses = []provision.FormAddress{{IP: c.ip, Hostname: c.host}}
			_, err := h.Mod.Provision.Create(bg, f, "admin")
			field := "addr_ip_0"
			if c.field == "hostname" {
				field = "addr_host_0"
			}
			m := refused(t, err)[field]
			if m.Key != c.key || m.Args["name"] != c.holder {
				t.Fatalf("%+v, want %s naming %q", m, c.key, c.holder)
			}
			if n := countServers(t, h); n != 2 {
				t.Fatalf("a refused form left %d servers", n)
			}
		})
	}
	if addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(addrs) != 0 {
		t.Fatalf("a refused add stored %+v", addrs)
	}
}

// An address the server itself already has, as an additional or a previous
// IP, is a duplicate, not "this IP already belongs to nl-1".
func TestAddressOfTheSameServerIsADuplicate(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	if err := h.Mod.Deploy.AddAddress(bg, id, "127.0.0.2", "alt.example.org", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.W.Exec(`INSERT INTO servers_previous_ips (server_id, ip, since, state_since) VALUES (?, '127.0.0.7', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, id); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"127.0.0.2", "127.0.0.7", "127.0.0.1"} {
		err := h.Mod.Deploy.AddAddress(bg, id, ip, "again.example.org", "admin")
		if m := refused(t, err)["ip"]; m.Key != "servers.err.addr_ip_dup" {
			t.Errorf("%s: %+v", ip, m)
		}
	}
	err := h.Mod.Deploy.AddAddress(bg, id, "127.0.0.3", "alt.example.org", "admin")
	if m := refused(t, err)["hostname"]; m.Key != "servers.err.addr_host_dup" {
		t.Errorf("own hostname: %+v", m)
	}
}

// Only the last additional address can be removed, so positions never shift.
func TestOnlyTheLastAddressCanBeRemoved(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	for i, ip := range []string{"127.0.0.2", "127.0.0.3"} {
		host := []string{"a.example.org", "b.example.org"}[i]
		if err := h.Mod.Deploy.AddAddress(bg, id, ip, host, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id)
	err := h.Mod.Deploy.RemoveAddress(bg, id, addrs[0].ID, "admin")
	if m := refused(t, err)["address"]; m.Key != "servers.err.addr_remove_last" {
		t.Fatalf("%+v", m)
	}
	if got, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(got) != 2 {
		t.Fatalf("a refused remove changed the addresses: %+v", got)
	}
	if ev := h.Events("server.address_removed"); len(ev) != 0 {
		t.Fatalf("a refused remove recorded %+v", ev)
	}
	if err := h.Mod.Deploy.RemoveAddress(bg, id, addrs[1].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	// The next add takes the freed position.
	if err := h.Mod.Deploy.AddAddress(bg, id, "127.0.0.4", "c.example.org", "admin"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(got) != 2 || got[1].Position != 3 {
		t.Fatalf("positions: %+v", got)
	}
}

// Two writers racing for one address: the check in the writing transaction
// lets exactly one through.
func TestAddressUniquenessIsCheckedInTheTransaction(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	a := h.Provisioned()
	b := h.AddServer("10.77.0.2")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int64{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = h.Mod.Deploy.AddAddress(bg, id, "127.0.0.9", []string{"a.example.org", "b.example.org"}[i], "admin")
		}()
	}
	wg.Wait()
	var ok, bad int
	for _, err := range errs {
		var fe provision.FieldErrors
		switch {
		case err == nil:
			ok++
		case errors.As(err, &fe):
			bad++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || bad != 1 {
		t.Fatalf("results %v", errs)
	}

	// Two creates with one additional address: one server, one refusal.
	errs = make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := h.Form()
			f.IP = []string{"127.0.0.21", "127.0.0.22"}[i]
			f.Addresses = []provision.FormAddress{{IP: "127.0.0.30", Hostname: "shared.example.org"}}
			_, errs[i] = h.Mod.Provision.Create(bg, f, "admin")
		}()
	}
	wg.Wait()
	ok, bad = 0, 0
	for _, err := range errs {
		var fe provision.FieldErrors
		switch {
		case err == nil:
			ok++
		case errors.As(err, &fe) && (fe["addr_ip_0"].Key != "" || fe["addr_host_0"].Key != ""):
			bad++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || bad != 1 {
		t.Fatalf("creates %v", errs)
	}
}

// An SSH connection IP equal to the primary IP is no separate address: it is
// stored empty, so SSH follows the primary IP through an IP change.
func TestSSHIPEqualToThePrimaryIPIsStoredEmpty(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	f := h.Form()
	f.SSHIP = " " + f.IP + " "
	id := h.Create(f)
	h.Drain()
	srv := active(t, h, id)
	if srv.SSHIP != "" || srv.ConnectIP() != f.IP {
		t.Fatalf("ssh ip %q, connect IP %q", srv.SSHIP, srv.ConnectIP())
	}
}

func countServers(t *testing.T, h *serverstest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.GetContext(bg, &n, `SELECT COUNT(*) FROM servers_servers`); err != nil {
		t.Fatal(err)
	}
	return n
}

package pages_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

func TestNewServerFormHasAddressRows(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	rec := h.Login.Get("/servers/new")
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	for _, want := range []string{`name="add_ip"`, `name="add_host"`, `data-addr-add`, "Additional addresses"} {
		mustContain(t, rec.Body.String(), want)
	}
}

func TestServerAddressesPageAddsAndRemoves(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()

	rec := h.Login.Get("/servers/" + sid(id) + "/addresses")
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), "Additional addresses", "Proxier does not write")

	rec = h.Login.Post("/servers/"+sid(id)+"/addresses", url.Values{"ip": {"127.0.0.2"}, "hostname": {"alt.example.org"}})
	if rec.Code != 303 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	addrs, err := store.ServerAddresses(bg, h.App.DB.R, id)
	if err != nil || len(addrs) != 1 || addrs[0].IP != "127.0.0.2" || addrs[0].Hostname != "alt.example.org" {
		t.Fatalf("addresses %+v %v", addrs, err)
	}

	// The primary address, a duplicate and a bad hostname are refused.
	for _, c := range []struct {
		name string
		vals url.Values
		want string
	}{
		{"the primary IP", url.Values{"ip": {"127.0.0.1"}, "hostname": {"second.example.org"}}, "Each additional address needs its own IP"},
		{"a taken hostname", url.Values{"ip": {"127.0.0.3"}, "hostname": {"alt.example.org"}}, "needs its own hostname"},
		{"not a hostname", url.Values{"ip": {"127.0.0.4"}, "hostname": {"not a host"}}, "full DNS name"},
	} {
		rec = h.Login.Post("/servers/"+sid(id)+"/addresses", c.vals)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), c.want) {
			t.Fatalf("%s: %d\n%s", c.name, rec.Code, rec.Body)
		}
	}
	if addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(addrs) != 1 {
		t.Fatalf("a refused add changed the addresses: %+v", addrs)
	}

	rec = h.Login.Post("/servers/"+sid(id)+"/addresses/"+sid(addrs[0].ID)+"/delete", nil)
	if rec.Code != 303 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(addrs) != 0 {
		t.Fatalf("addresses after remove: %+v", addrs)
	}
	if ev := h.Events("server.address_added"); len(ev) != 1 {
		t.Errorf("address_added events: %+v", ev)
	}
	if ev := h.Events("server.address_removed"); len(ev) != 1 {
		t.Errorf("address_removed events: %+v", ev)
	}
}

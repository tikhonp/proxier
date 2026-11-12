package pages_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// The refusal names the server that holds the IP, not a struct dump.
func TestAddressPageNamesTheServerThatHoldsTheIP(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	other := h.AddServer("10.77.0.2")
	name := h.Server(other).Name

	rec := h.Login.Post("/servers/"+sid(id)+"/addresses", url.Values{"ip": {"10.77.0.2"}, "hostname": {"x.example.org"}})
	body := rec.Body.String()
	if rec.Code != 422 || !strings.Contains(body, "This IP already belongs to "+name+".") {
		t.Fatalf("%d\n%s", rec.Code, body)
	}
	if strings.Contains(body, "belongs to {") {
		t.Fatalf("a struct dump in the message:\n%s", body)
	}
}

// Only the last additional address can go; the page answers 409 for another.
func TestAddressDeleteOfAnEarlierAddressIsRefused(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	if err := h.Mod.Deploy.AddAddress(bg, id, "127.0.0.2", "a.example.org", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Deploy.AddAddress(bg, id, "127.0.0.3", "b.example.org", "admin"); err != nil {
		t.Fatal(err)
	}
	addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id)
	rec := h.Login.Post("/servers/"+sid(id)+"/addresses/"+sid(addrs[0].ID)+"/delete", nil)
	if rec.Code != 409 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if got, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(got) != 2 {
		t.Fatalf("a refused remove changed the addresses: %+v", got)
	}
}

// A change that is stored but whose redeploy cannot start is no error page:
// the page says so and names the reason.
func TestAddressSavedButRedeployFailed(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	// The server's template version is gone, so the redeploy cannot be planned.
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET template_version = 99 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	rec := h.Login.Post("/servers/"+sid(id)+"/addresses", url.Values{"ip": {"127.0.0.2"}, "hostname": {"alt.example.org"}})
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "the redeploy did not start") || !strings.Contains(body, "/redeploy") {
		t.Fatalf("the page does not say the redeploy did not start:\n%s", body)
	}
	addrs, err := store.ServerAddresses(bg, h.App.DB.R, id)
	if err != nil || len(addrs) != 1 {
		t.Fatalf("addresses %+v %v", addrs, err)
	}

	rec = h.Login.Post("/servers/"+sid(id)+"/addresses/"+sid(addrs[0].ID)+"/delete", nil)
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(addrs) != 0 {
		t.Fatalf("the remove was not kept: %+v", addrs)
	}
}

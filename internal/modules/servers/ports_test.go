package servers_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
)

func TestEndpointCatalog(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	active := h.Provisioned()
	h.StopJobs() // the next server stays in provisioning
	f := h.Form()
	f.IP = "127.0.0.2"
	pending := h.Create(f)

	ctx := context.Background()
	cat := h.Mod.EndpointCatalog()
	list, err := cat.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ServerID != active || list[0].Name != "nl-1" || list[0].Health != "unknown" || list[0].HealthSince.IsZero() {
		t.Fatalf("active servers: %+v", list)
	}
	eps := list[0].Endpoints
	if len(eps) != 1 || eps[0].Key != "main" || eps[0].Host != "nl-1.hosts.tikhonnnnn.com" || eps[0].Port != 443 ||
		eps[0].Credential == "" || eps[0].Params["path"] == "" || eps[0].DisplayName != "🇳🇱 Netherlands 1" {
		t.Fatalf("endpoints: %+v", eps)
	}
	one, ok, err := cat.Server(ctx, active)
	if err != nil || !ok || one.Name != "nl-1" || len(one.Endpoints) != 1 {
		t.Fatalf("Server: %+v %v %v", one, ok, err)
	}
	// A server that is not active is not served, and neither is one that does not exist.
	for _, id := range []int64{pending, 4242} {
		if _, ok, err := cat.Server(ctx, id); ok || err != nil {
			t.Errorf("Server(%d) = %v, %v", id, ok, err)
		}
	}
}

func TestServerHostnamesNameTheServer(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	first := h.Provisioned()
	h.StopJobs()
	f := h.Form()
	f.IP = "127.0.0.2"
	second := h.Create(f) // still provisioning: listed too

	ctx := context.Background()
	list, err := h.Mod.ServerHostnames().Servers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ServerID != first || list[0].Name != "nl-1" || list[0].IP != "127.0.0.1" ||
		strings.Join(list[0].Hostnames, " ") != "nl-1.hosts.tikhonnnnn.com" ||
		list[1].ServerID != second || list[1].Name != "nl-2" || strings.Join(list[1].Hostnames, " ") != "nl-2.hosts.tikhonnnnn.com" {
		t.Fatalf("servers: %+v", list)
	}
	// A retired server's names are free to use again.
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET state = 'retired' WHERE id = ?`, second); err != nil {
		t.Fatal(err)
	}
	list, err = h.Mod.ServerHostnames().Servers(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "nl-1" {
		t.Fatalf("after retiring nl-2: %+v %v", list, err)
	}
}

func TestCatalogCarriesFlag(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	ctx := context.Background()
	list, err := h.Mod.EndpointCatalog().Active(ctx)
	if err != nil || len(list) != 1 || list[0].Flag != "🇳🇱" {
		t.Fatalf("active: %+v %v", list, err)
	}
	one, ok, err := h.Mod.EndpointCatalog().Server(ctx, id)
	if err != nil || !ok || one.Flag != "🇳🇱" {
		t.Fatalf("server: %+v %v %v", one, ok, err)
	}
}

func TestProxyDialerReturnsDialFunc(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	ctx := context.Background()
	dial, closer, err := h.Mod.ProxyDialer().Dial(ctx, id)
	if err != nil || dial == nil || closer == nil {
		t.Fatalf("dial: %v", err)
	}
	// the root package's type: routing needs no other servers package
	if reflect.TypeOf(dial) != reflect.TypeFor[servers.DialFunc]() {
		t.Errorf("Dial returns a %T", dial)
	}
	if err := closer.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if _, _, err := h.Mod.ProxyDialer().Dial(ctx, 4242); err == nil {
		t.Error("a missing server dialed")
	}
}

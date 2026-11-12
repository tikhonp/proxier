package health_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// A previous IP was the primary address, so only the primary endpoints are
// tested through it. An endpoint on an additional address never lived there;
// aimed at the previous IP it would fail and hide that the IP works.
func TestPreviousIPTestSkipsEndpointsOnAdditionalAddresses(t *testing.T) {
	f := newFixture(t)
	const host = "alt.example.org"
	if err := f.Mod.Deploy.AddAddress(bg, f.ID, "10.77.0.9", host, "member:1"); err != nil {
		t.Fatal(err)
	}
	f.AddEndpoint("alt")
	if _, err := f.App.DB.W.Exec(`UPDATE servers_endpoints SET host = ? WHERE server_id = ? AND key = 'alt'`, host, f.ID); err != nil {
		t.Fatal(err)
	}
	f.ProxyAccepts(func(e endpoint.Endpoint) bool { return e.Host != host })
	f.keepPrevious(prevIP)

	f.testPrevious()
	r, ok, err := store.LatestPreviousResult(bg, f.App.DB.R, f.prev().ID)
	if err != nil || !ok || !r.OK {
		t.Fatalf("result %+v %v", r, err)
	}
	d := health.ParsePreviousDetail(r.Detail)
	if len(d.Endpoints) != 1 || d.Endpoints[0].Key != "main" {
		t.Fatalf("tested endpoints: %+v", d.Endpoints)
	}
}

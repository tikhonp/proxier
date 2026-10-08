package pages_test

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// routedGuard covers every hostname under tikhonnnnn.com, as a routing list
// holding that suffix would.
type routedGuard struct{ asked []string }

func (g *routedGuard) Covering(_ context.Context, hostnames []string) ([]servers.RoutedName, error) {
	g.asked = append(g.asked, hostnames...)
	var out []servers.RoutedName
	for _, h := range hostnames {
		if strings.HasSuffix(h, ".tikhonnnnn.com") {
			out = append(out, servers.RoutedName{Hostname: h, Domain: "tikhonnnnn.com", Service: "my-sites", List: "Main"})
		}
	}
	return out, nil
}

func TestNewServerRefusedWhenRouted(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	g := &routedGuard{}
	h.Mod.SetRouting(g)
	f := h.Form()
	form := url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "root_password": {f.RootPassword}, "location": {sid(h.LocationID)},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "param.letsencrypt_email": {"me@example.com"},
	}
	const want = "nl-1.hosts.tikhonnnnn.com is covered by tikhonnnnn.com (my-sites) in Main: Proxier&#39;s checks would go into the tunnel. Take it out of the list first."

	// the live summary says it while the admin types
	rec := h.Login.Post("/servers/new/summary", url.Values{
		"ip": {"203.0.113.24"}, "location": {sid(h.LocationID)}, "location_auto": {"0"},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {sid(h.TemplateID) + ":1"},
	})
	mustContain(t, rec.Body.String(), want)

	rec = h.Login.Post("/servers", form)
	if rec.Code != 422 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), want)
	if srvs, _ := store.ListServers(bg, h.App.DB.R); len(srvs) != 0 {
		t.Fatalf("created anyway: %+v", srvs)
	}
	if len(g.asked) == 0 || g.asked[len(g.asked)-1] != "nl-1.hosts.tikhonnnnn.com" {
		t.Errorf("asked %v", g.asked)
	}

	// without a guard nothing is checked
	h.Mod.SetRouting(nil)
	rec = h.Login.Post("/servers", form)
	if rec.Code != 303 {
		t.Fatalf("without a guard: %d\n%s", rec.Code, rec.Body)
	}
}

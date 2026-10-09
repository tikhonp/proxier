package generations_test

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// TestRouterSetUpEndToEnd is the roadmap's Phase 4 exit short of a real
// router: publish, generate with a new link and a new router, the router
// fetches its file once, the probe connects with Proxier's key and the
// initial sync installs Main's services.
func TestRouterSetUpEndToEnd(t *testing.T) {
	m := rscriptstest.WithModules(t)
	m.StartJobs()

	// Main holds a service; the fake router accepts Proxier's key only
	m.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	sid, err := m.Routing.Services.Add(bg, "v2fly:openai", "admin")
	if err != nil {
		t.Fatal(err)
	}
	main, err := m.Routing.Lists.Default(bg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Routing.Lists.Add(bg, main.ID, []int64{sid}, "admin"); err != nil {
		t.Fatal(err)
	}
	line, _ := keyLine(t, m.Harness)
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	r := routerostest.New(t, key)
	host, port, err := net.SplitHostPort(r.Addr)
	if err != nil {
		t.Fatal(err)
	}

	// publish and generate "Parents"
	id := m.Script("fresh-router", paramstest.Annotated())
	f := realForm(t, m, id, m.Subscription("Family"), host)
	f.Router.Port, _ = strconv.Atoi(port)
	gid := generate(t, m.Harness, id, f)
	g, err := m.Mod.Generations.Get(bg, gid)
	if err != nil || g.Router == nil || g.Router.State != "awaiting" || g.Link == nil {
		t.Fatalf("generated: %+v %v", g, err)
	}

	// the router fetches its file once
	_, link, err := m.Mod.Generations.CreateFetchURL(bg, gid, events.ActorAdmin)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(link, "http://proxier.test")
	get := func() *httpResult {
		res := m.Site.Do(sitetest.Req{Path: path, Header: http.Header{"User-Agent": {"Mikrotik/7.24.5 Fetch"}}, Addr: "198.51.100.4:51000"})
		return &httpResult{Code: res.Code, Body: res.Body.String()}
	}
	file := get()
	token, err := m.Subs.Links.Token(bg, g.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`:local subUrl "` + m.Subs.Links.URL(token) + `"`, `:local vpnList "to_vpn_list"`,
		`:local dohForwarder "vpn-doh"`, `:local proxierKey "` + line + `"`} {
		if file.Code != http.StatusOK || !strings.Contains(file.Body, want) {
			t.Fatalf("the file (%d) has no %s", file.Code, want)
		}
	}
	if again := get(); again.Code != http.StatusNotFound {
		t.Fatalf("second fetch: %d", again.Code)
	}
	if ev := m.Events("routerscript.fetched"); len(ev) != 1 || ev[0].Payload["ip"] != "198.51.100.4" {
		t.Fatalf("fetched: %+v", ev)
	}

	// the probe connects (the imported script installed the key) and the initial sync runs
	m.RunSchedule(routers.JobProbeRound)
	m.Drain()
	rt, err := m.Routing.Routers.Get(bg, g.RouterID)
	if err != nil || rt.State != routers.StateActive || !rt.Connected() {
		t.Fatalf("router: %+v %v", rt, err)
	}
	if k, err := m.App.SSH.KnownHost(bg, r.Addr); err != nil || k.Subject != routers.RouterSubject(g.RouterID) {
		t.Fatalf("its key isn't pinned: %+v %v", k, err)
	}
	if ev := m.Events("routing.router_connected"); len(ev) != 1 {
		t.Fatalf("router_connected: %+v", ev)
	}
	if names := r.Names("openai"); len(names) != 2 {
		t.Fatalf("Main's services on the router: %v (tags %v)", names, r.Tags())
	}

	page := m.Login.Get("/router-scripts/generations/1").Body.String()
	for _, want := range []string{"fetched at 15:00 from 198.51.100.4 · Mikrotik/7.24.5 Fetch", "connected at 15:00 · synced at 15:00", "used at 15:00"} {
		if !strings.Contains(page, want) {
			t.Errorf("After the import: no %q", want)
		}
	}
	if strings.Contains(page, `hx-trigger="every 10s"`) {
		t.Error("still polling")
	}
}

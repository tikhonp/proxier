package pages_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// members is a routing site with anna and boris at routing: own, vera with
// the Viewer preset, and what anna owns: a list holding her private custom
// service and a shared one, a router following it, a Shadowrocket config
// and a discovery run.
type members struct {
	h                             *routingtest.Harness
	anna, boris, vera             *sitetest.Login
	annaW, borisW                 access.Who
	list, private, shared, router int64
	config, run, borisList        int64
}

func ownGrant() access.Grant { return access.Grant{}.With(access.Routing, access.LevelOwn) }

func newMembers(t *testing.T) members {
	t.Helper()
	h := routingtest.New(t)
	ctx := context.Background()
	m := members{h: h}
	m.anna = h.Site.Join("anna", ownGrant(), "")
	m.boris = h.Site.Join("boris", ownGrant(), "")
	m.vera = h.Site.Join("vera", access.PresetViewer.Grant(), "")
	who := func(name string) access.Who {
		mem, err := h.App.Auth.MemberByUsername(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return access.Who{Member: mem.ID, Grant: ownGrant()}
	}
	m.annaW, m.borisW = who("anna"), who("boris")
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	m.shared = h.Upstream("v2fly:anthropic")
	m.private = h.CustomOf(m.annaW, "annasite", "anna.example")
	m.list = h.ListOf(m.annaW, "Anna list", m.private, m.shared)
	m.borisList = h.ListOf(m.borisW, "Boris list")
	_, m.router = h.Router("anna-router", m.list, routingtest.OwnedBy(m.annaW))
	var err error
	if m.config, err = h.Mod.Shadowrocket.Create(ctx, m.annaW, shadowrocket.New{Name: "anna-phone", ListID: m.list, Policy: "PROXY", Base: "[Rule]\n"}, "member:2"); err != nil {
		t.Fatal(err)
	}
	if m.run, err = h.Mod.Discovery.Start(ctx, discoveryStart(m.annaW.Member), "member:2"); err != nil {
		t.Fatal(err)
	}
	return m
}

func path(prefix string, id int64) string { return prefix + "/" + strconv.FormatInt(id, 10) }

// Another member's list, router, Shadowrocket config, custom service or
// discovery run is not found at routing: own, not forbidden, so IDs don't
// tell what exists; the lists, search and pickers leave them out.
func TestOwnMemberOpeningAnotherMembersThingGets404(t *testing.T) {
	m := newMembers(t)
	for _, p := range []string{
		path("/routing/lists", m.list), path("/routing/routers", m.router), path("/routing/shadowrocket", m.config),
		path("/routing/services", m.private), path("/routing/discover", m.run),
		path("/routing/lists", m.list) + "/edit", path("/routing/routers", m.router) + "/edit",
	} {
		if rec := m.boris.Get(p); rec.Code != http.StatusNotFound {
			t.Errorf("boris GET %s: %d, want 404", p, rec.Code)
		}
		if rec := m.anna.Get(p); rec.Code != http.StatusOK {
			t.Errorf("anna GET %s: %d", p, rec.Code)
		}
	}
	for _, p := range []string{path("/routing/lists", m.list) + "/edit", path("/routing/routers", m.router) + "/sync", path("/routing/shadowrocket", m.config) + "/disable"} {
		if rec := m.boris.Post(p, url.Values{"name": {"mine"}}); rec.Code != http.StatusNotFound {
			t.Errorf("boris POST %s: %d, want 404", p, rec.Code)
		}
	}
	for p, name := range map[string]string{
		"/routing/lists": ">Anna list<", "/routing/routers": ">anna-router<", "/routing/shadowrocket": ">anna-phone<",
		"/routing/services": ">annasite<", "/routing/discover": "anna.example", "/routing/routers/new": "Anna list",
		"/routing/shadowrocket/new": "Anna list",
	} {
		rec := m.boris.Get(p)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), name) {
			t.Errorf("boris GET %s: %d, shows %s", p, rec.Code, name)
		}
	}
	if !strings.Contains(m.boris.Get("/routing/services").Body.String(), ">anthropic<") {
		t.Error("a shared service is every routing member's")
	}
	if hits, _ := m.h.Mod.Search(context.Background(), m.borisW, "anna", 10); len(hits) != 0 {
		t.Errorf("boris finds %+v", hits)
	}
}

// A list may hold only services its changer may use: another member's
// custom service is private, and a router follows only a list its owner may
// change.
func TestAnotherMembersPrivateServiceCantJoinAList(t *testing.T) {
	m := newMembers(t)
	add := path("/routing/lists", m.borisList) + "/services"
	if rec := m.boris.Post(add, url.Values{"service": {strconv.FormatInt(m.private, 10)}}); rec.Code != http.StatusNotFound {
		t.Errorf("boris adds anna's service: %d", rec.Code)
	}
	if rec := m.boris.Post(add, url.Values{"service": {strconv.FormatInt(m.shared, 10)}}); rec.Code != http.StatusSeeOther {
		t.Errorf("boris adds a shared service: %d", rec.Code)
	}
	rec := m.boris.Post("/routing/routers", url.Values{"name": {"boris-router"}, "list": {strconv.FormatInt(m.list, 10)}, "host": {"10.0.0.1"}, "user": {"proxier"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a router of boris following anna's list: %d", rec.Code)
	}
}

// A custom service's tag is its identity on routers, so it is unique across
// every member; the refusal never names the private service or its owner.
func TestCustomTagTakenByAnotherMembersServiceIsRefusedWithoutNamingIt(t *testing.T) {
	m := newMembers(t)
	rec := m.boris.Post("/routing/services/custom", url.Values{"name": {"My sites"}, "tag": {"annasite"}})
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "Another service has this tag.") {
		t.Fatalf("taken tag: %d", rec.Code)
	}
	if strings.Contains(body, "anna") && strings.Contains(body, ">anna<") || strings.Contains(body, "anna.example") {
		t.Error("the refusal names anna's service")
	}
}

// view_all reads everyone's lists, routers and configs, changes nothing of
// theirs (403, and no controls), and still doesn't see a private service.
func TestViewerReadsButChangesNothingOfOthers(t *testing.T) {
	m := newMembers(t)
	changes := func() int {
		return len(m.h.Events("routing.list_updated")) + len(m.h.Events("routing.router_paused")) + len(m.h.Events("routing.shadowrocket_updated"))
	}
	before := changes()
	controls := []string{path("/routing/lists", m.list) + `/add"`, path("/routing/lists", m.list) + `/edit"`, `href="` + path("/routing/services", m.private) + `"`}
	annas := m.anna.Get(path("/routing/lists", m.list)).Body.String()
	rec := m.vera.Get(path("/routing/lists", m.list))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "annasite") {
		t.Errorf("vera's view of anna's list: %d", rec.Code)
	}
	for _, c := range controls {
		if !strings.Contains(annas, c) || strings.Contains(body, c) {
			t.Errorf("%s: anna's page has it %v, vera's %v", c, strings.Contains(annas, c), strings.Contains(body, c))
		}
	}
	if !strings.Contains(body, "owner anna") {
		t.Error("the list doesn't name its owner")
	}
	sync := path("/routing/routers", m.router) + `/sync"`
	if !strings.Contains(m.anna.Get(path("/routing/routers", m.router)).Body.String(), sync) {
		t.Error("anna's router page has no Sync now")
	}
	if rec := m.vera.Get(path("/routing/routers", m.router)); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), sync) {
		t.Errorf("vera's view of anna's router: %d", rec.Code)
	}
	if rec := m.vera.Get(path("/routing/services", m.private)); rec.Code != http.StatusNotFound {
		t.Errorf("vera opens anna's private service: %d", rec.Code)
	}
	for _, p := range []string{
		path("/routing/lists", m.list) + "/edit", path("/routing/routers", m.router) + "/sync",
		path("/routing/routers", m.router) + "/pause", path("/routing/shadowrocket", m.config) + "/disable",
		path("/routing/shadowrocket", m.config) + "/edit",
	} {
		if rec := m.vera.Post(p, url.Values{"name": {"x"}}); rec.Code != http.StatusForbidden {
			t.Errorf("vera POST %s: %d, want 403", p, rec.Code)
		}
	}
	if rec := m.vera.Get(path("/routing/lists", m.list) + "/edit"); rec.Code != http.StatusForbidden {
		t.Errorf("vera's edit page of anna's list: %d", rec.Code)
	}
	if n := changes() - before; n != 0 {
		t.Errorf("%d changes recorded", n)
	}
}

// Shared services (v2fly, iplist, url), the default list and the import
// need routing: manage_all.
func TestSharedThingsNeedManageAll(t *testing.T) {
	m := newMembers(t)
	for _, p := range []string{
		path("/routing/services", m.shared) + "/remove", path("/routing/services", m.shared) + "/refresh",
		path("/routing/services", m.shared) + "/switch", path("/routing/lists", m.borisList) + "/default",
		path("/routing/lists", m.borisList) + "/refresh", "/routing/import",
	} {
		if rec := m.boris.Post(p, url.Values{"selector": {"v2fly:anthropic"}}); rec.Code != http.StatusForbidden {
			t.Errorf("boris POST %s: %d, want 403", p, rec.Code)
		}
	}
	page := path("/routing/services", m.shared)
	if body := m.h.Login.Get(page).Body.String(); !strings.Contains(body, page+`/refresh"`) || !strings.Contains(body, page+`/remove"`) {
		t.Fatal("the owner's page of a shared service has no Refresh now or Remove")
	}
	if rec := m.boris.Get(page); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), page+`/refresh"`) ||
		strings.Contains(rec.Body.String(), page+`/remove"`) {
		t.Errorf("boris's view of a shared service: %d", rec.Code)
	}
	if rec := m.boris.Get("/routing/services"); strings.Contains(rec.Body.String(), `href="/routing/services/add"`) {
		t.Error("Add service is offered below manage_all")
	}
	if rec := m.h.Login.Post(path("/routing/services", m.shared)+"/refresh", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("the owner refreshes: %d", rec.Code)
	}
}

func discoveryStart(owner int64) discovery.Start {
	return discovery.Start{Owner: owner, Website: "anna.example", Via: discovery.ViaDirect}
}

// A Test connection from the add form shows a host and its fingerprints, so
// it is its starter's: another member at own gets 404 for it and can't
// confirm a key through it.
func TestAnotherMembersConnectionTestIsNotFound(t *testing.T) {
	m := newMembers(t)
	id, err := m.h.Mod.Routers.StartTest(context.Background(), 0, m.annaW, routers.Connection{Host: "10.230.9.1", Port: 22, User: "proxier"}, "member:2")
	if err != nil {
		t.Fatal(err)
	}
	p := "/routing/routers/tests/" + strconv.FormatInt(id, 10)
	htmx := http.Header{"Hx-Request": {"true"}}
	if rec := m.anna.Get(p, htmx); rec.Code != http.StatusOK {
		t.Fatalf("anna's own test: %d", rec.Code)
	}
	if rec := m.boris.Get(p, htmx); rec.Code != http.StatusNotFound {
		t.Errorf("boris GET %s: %d, want 404", p, rec.Code)
	}
	if rec := m.boris.Post(p+"/confirm", url.Values{"decision": {"trust"}}); rec.Code != http.StatusNotFound {
		t.Errorf("boris confirming anna's test: %d, want 404", rec.Code)
	}
}

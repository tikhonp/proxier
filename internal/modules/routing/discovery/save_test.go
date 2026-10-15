package discovery_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/platform/access"
)

// homeRun is a finished run of tikhonnnnn.com that also loads example.org.
func homeRun(t *testing.T, h *routingtest.Harness, b interface {
	Site(string, string, discovery.Result)
}) int64 {
	t.Helper()
	url := "https://tikhonnnnn.com/"
	b.Site(url, "", loaded(url, "Tikhon's site",
		req(url, "tikhonnnnn.com"), req("https://www.tikhonnnnn.com/a", "www.tikhonnnnn.com"),
		req("https://assets.example.org/x.css", "assets.example.org"), req("https://cdn.example.org/y.js", "cdn.example.org")))
	return wait(t, h, start(t, h, discovery.Start{Owner: 1, Website: "tikhonnnnn.com", Via: discovery.ViaDirect}), discovery.Done).ID
}

func TestCreateRefusedByGuard(t *testing.T) {
	h, b := newH(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	id := homeRun(t, h, b)
	before := len(h.Marks.Changes())

	_, err := h.Mod.Discovery.Create(bg, id, access.All, services.Custom{Owner: 1, Name: "Mine"}, discovery.Pick{Suffix: []string{"tikhonnnnn.com", "example.org"}}, []int64{1}, "admin")
	var ge *lists.GuardError
	if !errors.As(err, &ge) || ge.Server != "nl-1" || ge.Hostname != "nl-1.hosts.tikhonnnnn.com" || ge.Domain != "tikhonnnnn.com" || ge.List != "Main" {
		t.Fatalf("create: %v", err)
	}
	if _, err := h.Mod.Services.ByTag(bg, "mine", access.All); !errors.Is(err, services.ErrNotFound) {
		t.Errorf("a service was created: %v", err)
	}
	if ev := h.Events("routing.list_refused_server_hostname"); len(ev) != 1 || ev[0].Payload["server"] != "nl-1" {
		t.Errorf("refusals: %+v", ev)
	}

	// without it: the service in the chosen lists, origin discovery, the lists marked
	parents := h.List("Parents")
	sid, err := h.Mod.Discovery.Create(bg, id, access.All, services.Custom{Owner: 1, Name: "Example assets"},
		discovery.Pick{Suffix: []string{"example.org"}, Exact: []string{"www.tikhonnnnn.com", "assets.example.org", "not.in.the.run"}}, []int64{1, parents}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	it, err := h.Mod.Services.Get(bg, sid, access.All)
	if err != nil || it.Tag != "example-assets" || it.Origin != "discovery" || it.Name != "Example assets" {
		t.Fatalf("service: %+v %v", it, err)
	}
	snap, err := h.Mod.Services.Accepted(bg, sid)
	if err != nil {
		t.Fatal(err)
	}
	// assets.example.org, ticked under example.org, stays (ADR 0016); a name the run never saw is dropped
	if !slices.Equal(snap.Set.Suffix, []string{"example.org"}) || !slices.Equal(snap.Set.Exact, []string{"assets.example.org", "www.tikhonnnnn.com"}) {
		t.Errorf("names: %v %v", snap.Set.Suffix, snap.Set.Exact)
	}
	ls, err := h.Mod.Lists.ListsOf(bg, sid, access.All)
	if err != nil || len(ls) != 2 {
		t.Fatalf("lists: %+v %v", ls, err)
	}
	marked := map[int64]bool{}
	for _, c := range h.Marks.Changes()[before:] {
		for _, l := range c.Lists {
			marked[l] = true
		}
	}
	if !marked[1] || !marked[parents] {
		t.Errorf("marked: %v", marked)
	}
	if ev := h.Events("routing.service_added"); len(ev) != 1 || ev[0].Payload["origin"] != "discovery" {
		t.Errorf("service_added: %+v", ev)
	}

	if _, err := h.Mod.Discovery.Create(bg, id, access.All, services.Custom{Owner: 1, Name: "Nothing"}, discovery.Pick{}, nil, "admin"); !errors.Is(err, discovery.ErrNoPicks) {
		t.Errorf("no picks: %v", err)
	}
}

func TestAddToExisting(t *testing.T) {
	h, b := newH(t)
	id := homeRun(t, h, b)
	mine := h.Custom("mine", "a.com", "full:cdn.example.org")
	h.List("Main", mine)

	err := h.Mod.Discovery.AddTo(bg, id, access.All, mine, discovery.Pick{Suffix: []string{"example.org"}, Exact: []string{"www.tikhonnnnn.com", "assets.example.org"}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := h.Mod.Services.Accepted(bg, mine)
	if err != nil {
		t.Fatal(err)
	}
	// cdn.example.org and assets.example.org stay under example.org (ADR 0016)
	if !slices.Equal(snap.Set.Suffix, []string{"a.com", "example.org"}) || !slices.Equal(snap.Set.Exact, []string{"assets.example.org", "cdn.example.org", "www.tikhonnnnn.com"}) {
		t.Errorf("names: %v %v", snap.Set.Suffix, snap.Set.Exact)
	}
	// an upstream service can't take picks
	up := "v2fly:anthropic"
	h.Up.V2fly("anthropic", "claude.ai\n")
	upID := h.Upstream(up)
	if err := h.Mod.Discovery.AddTo(bg, id, access.All, upID, discovery.Pick{Suffix: []string{"example.org"}}, "admin"); !errors.Is(err, discovery.ErrNotCustom) {
		t.Errorf("into an upstream service: %v", err)
	}
	// the guard applies as in the editor
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	err = h.Mod.Discovery.AddTo(bg, id, access.All, mine, discovery.Pick{Suffix: []string{"tikhonnnnn.com"}}, "admin")
	var ge *lists.GuardError
	if !errors.As(err, &ge) || ge.Server != "nl-1" {
		t.Errorf("guard: %v", err)
	}
}

// A run is its starter's: another member at own doesn't find it, a reader
// can't save from it, and what its owner saves is theirs.
func TestRunsBelongToWhoStartedThem(t *testing.T) {
	h, b := newH(t)
	id := homeRun(t, h, b)
	own := func(m int64) access.Scope {
		return access.Who{Member: m, Grant: access.Grant{}.With(access.Routing, access.LevelOwn)}.Scope(access.Routing)
	}
	pick := discovery.Pick{Suffix: []string{"example.org"}}
	if _, err := h.Mod.Discovery.Get(bg, id, own(2)); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("another member's run: %v", err)
	}
	if runs, _ := h.Mod.Discovery.Recent(bg, own(2), 10); len(runs) != 0 {
		t.Fatalf("another member's runs listed: %d", len(runs))
	}
	viewer := access.Who{Member: 2, Grant: access.PresetViewer.Grant()}.Scope(access.Routing)
	var forbidden *access.ForbiddenError
	if _, err := h.Mod.Discovery.Create(bg, id, viewer, services.Custom{Name: "Mine"}, pick, nil, "member:2"); !errors.As(err, &forbidden) {
		t.Fatalf("a reader saves from member 1's run: %v", err)
	}
	sid, err := h.Mod.Discovery.Create(bg, id, own(1), services.Custom{Name: "Mine"}, pick, nil, "member:1")
	if err != nil {
		t.Fatal(err)
	}
	if it, err := h.Mod.Services.Get(bg, sid, own(1)); err != nil || it.OwnerID != 1 {
		t.Fatalf("the service is the run's owner's: %+v %v", it, err)
	}
}

package discovery_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
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
	return wait(t, h, start(t, h, discovery.Start{Website: "tikhonnnnn.com", Via: discovery.ViaDirect}), discovery.Done).ID
}

func TestCreateRefusedByGuard(t *testing.T) {
	h, b := newH(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	id := homeRun(t, h, b)
	before := len(h.Marks.Changes())

	_, err := h.Mod.Discovery.Create(bg, id, services.Custom{Name: "Mine"}, discovery.Pick{Suffix: []string{"tikhonnnnn.com", "example.org"}}, []int64{1}, "admin")
	var ge *lists.GuardError
	if !errors.As(err, &ge) || ge.Server != "nl-1" || ge.Hostname != "nl-1.hosts.tikhonnnnn.com" || ge.Domain != "tikhonnnnn.com" || ge.List != "Main" {
		t.Fatalf("create: %v", err)
	}
	if _, err := h.Mod.Services.ByTag(bg, "mine"); !errors.Is(err, services.ErrNotFound) {
		t.Errorf("a service was created: %v", err)
	}
	if ev := h.Events("routing.list_refused_server_hostname"); len(ev) != 1 || ev[0].Payload["server"] != "nl-1" {
		t.Errorf("refusals: %+v", ev)
	}

	// without it: the service in the chosen lists, origin discovery, the lists marked
	parents := h.List("Parents")
	sid, err := h.Mod.Discovery.Create(bg, id, services.Custom{Name: "Example assets"},
		discovery.Pick{Suffix: []string{"example.org"}, Exact: []string{"www.tikhonnnnn.com", "assets.example.org", "not.in.the.run"}}, []int64{1, parents}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	it, err := h.Mod.Services.Get(bg, sid)
	if err != nil || it.Tag != "example-assets" || it.Origin != "discovery" || it.Name != "Example assets" {
		t.Fatalf("service: %+v %v", it, err)
	}
	snap, err := h.Mod.Services.Accepted(bg, sid)
	if err != nil {
		t.Fatal(err)
	}
	// assets.example.org is under example.org; a name the run never saw is dropped
	if !slices.Equal(snap.Set.Suffix, []string{"example.org"}) || !slices.Equal(snap.Set.Exact, []string{"www.tikhonnnnn.com"}) {
		t.Errorf("names: %v %v", snap.Set.Suffix, snap.Set.Exact)
	}
	ls, err := h.Mod.Lists.ListsOf(bg, sid)
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

	if _, err := h.Mod.Discovery.Create(bg, id, services.Custom{Name: "Nothing"}, discovery.Pick{}, nil, "admin"); !errors.Is(err, discovery.ErrNoPicks) {
		t.Errorf("no picks: %v", err)
	}
}

func TestAddToExisting(t *testing.T) {
	h, b := newH(t)
	id := homeRun(t, h, b)
	mine := h.Custom("mine", "a.com", "full:cdn.example.org")
	h.List("Main", mine)

	err := h.Mod.Discovery.AddTo(bg, id, mine, discovery.Pick{Suffix: []string{"example.org"}, Exact: []string{"www.tikhonnnnn.com", "assets.example.org"}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := h.Mod.Services.Accepted(bg, mine)
	if err != nil {
		t.Fatal(err)
	}
	// the editor's absorption: cdn.example.org and assets.example.org go under example.org
	if !slices.Equal(snap.Set.Suffix, []string{"a.com", "example.org"}) || !slices.Equal(snap.Set.Exact, []string{"www.tikhonnnnn.com"}) {
		t.Errorf("names: %v %v", snap.Set.Suffix, snap.Set.Exact)
	}
	// an upstream service can't take picks
	up := "v2fly:anthropic"
	h.Up.V2fly("anthropic", "claude.ai\n")
	upID := h.Upstream(up)
	if err := h.Mod.Discovery.AddTo(bg, id, upID, discovery.Pick{Suffix: []string{"example.org"}}, "admin"); !errors.Is(err, discovery.ErrNotCustom) {
		t.Errorf("into an upstream service: %v", err)
	}
	// the guard applies as in the editor
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	err = h.Mod.Discovery.AddTo(bg, id, mine, discovery.Pick{Suffix: []string{"tikhonnnnn.com"}}, "admin")
	var ge *lists.GuardError
	if !errors.As(err, &ge) || ge.Server != "nl-1" {
		t.Errorf("guard: %v", err)
	}
}

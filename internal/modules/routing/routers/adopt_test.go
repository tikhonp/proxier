package routers_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
)

func TestAdopt(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", lines("nf", 26))
	withCatalog(t, h)
	main := mainList(t, h)
	h.List("Main", h.Custom("mine", "example.org"))
	r, id := h.Router("Home", 0)
	r.Seed("netflix", entries(customNames("nf", 25)...)...)
	r.Seed("old-work", entries("work.example", "full:vpn.other.example")...)
	syncNow(t, h, id)

	// as the catalog's v2fly:netflix: the list gains it and the sync updates the tag
	h.StartJobs()
	sid, err := h.Mod.Routers.Adopt(bg, id, "netflix", routers.AdoptV2fly, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Settle()
	it, err := h.Mod.Services.Get(bg, sid)
	if err != nil || it.Selector != "v2fly:netflix" {
		t.Fatalf("service: %+v %v", it, err)
	}
	if ls, _ := h.Mod.Lists.ListsOf(bg, sid); len(ls) != 1 || ls[0].ID != main {
		t.Fatalf("in Main: %+v", ls)
	}
	if a := applied(t, h, id); a["netflix"].Suffix != 26 {
		t.Fatalf("applied: %+v", a)
	}
	if len(r.Names("netflix")) != 26 {
		t.Fatalf("updated: %d", len(r.Names("netflix")))
	}
	if _, ok := unmanagedTags(t, h, id)["netflix"]; ok {
		t.Fatal("no longer unmanaged")
	}

	// as a custom service made from the router's names: recorded as is
	cid, err := h.Mod.Routers.Adopt(bg, id, "old-work", routers.AdoptCustom, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Settle()
	c, err := h.Mod.Services.Get(bg, cid)
	if err != nil || c.Tag != "old-work" || c.Source != selector.Custom || c.Origin != "router" {
		t.Fatalf("custom: %+v %v", c, err)
	}
	if a := applied(t, h, id); a["old-work"].Suffix != 1 || a["old-work"].Exact != 1 {
		t.Fatalf("recorded: %+v", a)
	}
	if s := lastSync(t, h, id); s.Recorded != 1 || s.Pushed() != 0 {
		t.Fatalf("nothing pushed: %+v", s)
	}
	if !slices.Equal(r.Names("old-work"), []string{"vpn.other.example", "work.example"}) {
		t.Fatalf("router: %v", r.Names("old-work"))
	}
	if len(unmanagedTags(t, h, id)) != 0 {
		t.Fatal("nothing unmanaged")
	}
}

func TestInvalidTagCantBeAdopted(t *testing.T) {
	h := routingtest.New(t)
	h.List("Main", h.Custom("mine", "example.org"))
	r, id := h.Router("Home", 0)
	r.Seed("my stuff", entries("stuff.example")...)
	syncNow(t, h, id)
	u, ok := unmanagedTags(t, h, id)["my stuff"]
	if !ok || routers.Adoptable(u.Tag) {
		t.Fatalf("listed, not adoptable: %+v", u)
	}
	for _, how := range []string{routers.AdoptCustom, routers.AdoptV2fly, routers.AdoptExisting} {
		if _, err := h.Mod.Routers.Adopt(bg, id, "my stuff", how, "admin"); !errors.Is(err, routers.ErrInvalidTag) {
			t.Fatalf("%s: %v", how, err)
		}
	}
	// remove and ignore still work
	if err := h.Mod.Routers.Ignore(bg, id, "my stuff", true, "admin"); err != nil {
		t.Fatal(err)
	}
	h.StartJobs()
	if _, err := h.Mod.Routers.RemoveUnmanaged(bg, id, "my stuff", "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	if len(r.Names("my stuff")) != 0 {
		t.Fatal("removed")
	}
}

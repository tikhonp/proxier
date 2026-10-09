package routers_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

// withCatalog refreshes the catalog from what the upstream serves now.
func withCatalog(t *testing.T, h *routingtest.Harness) {
	t.Helper()
	h.StartJobs()
	if _, err := h.Mod.Catalog.RefreshNow(bg, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
}

func unmanagedTags(t *testing.T, h *routingtest.Harness, id int64) map[string]routers.UnmanagedTag {
	t.Helper()
	us, err := h.Mod.Routers.Unmanaged(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]routers.UnmanagedTag{}
	for _, u := range us {
		out[u.Tag] = u
	}
	return out
}

func TestUnmanagedTagFoundAndOffered(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", lines("nf", 26))
	withCatalog(t, h)
	h.List("Main", h.Custom("mine", "example.org"))
	r, id := h.Router("Home", 0)
	r.Seed("netflix", entries(customNames("nf", 26)...)...)

	syncNow(t, h, id)
	u := unmanagedTags(t, h, id)
	nf, ok := u["netflix"]
	if !ok || len(u) != 1 || nf.Entries != 26 || len(nf.Names) != 26 || nf.Ignored || nf.FirstSeen.IsZero() {
		t.Fatalf("netflix is unmanaged with its 26 entries: %+v", u)
	}
	if len(r.Names("netflix")) != 26 {
		t.Fatal("left alone by syncs")
	}
	if !slices.Contains(nf.Catalog, "v2fly:netflix") || nf.Existing != nil {
		t.Fatalf("Adopt offers v2fly:netflix: %+v", nf)
	}
	ev := h.Events("routing.unmanaged_tags_found")
	if len(ev) != 1 || ev[0].Payload["tags"] != "netflix" || !notifies(t, ev[0]) {
		t.Fatalf("unmanaged_tags_found{netflix}: %+v", ev)
	}
	syncNow(t, h, id)
	if _, err := h.Mod.Routers.Preview(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	if len(h.Events("routing.unmanaged_tags_found")) != 1 || len(r.Names("netflix")) != 26 {
		t.Fatal("once: the same tags again record nothing")
	}
	// a tag that leaves and comes back notifies again
	r.DeleteEntries("netflix", 26)
	syncNow(t, h, id)
	if len(unmanagedTags(t, h, id)) != 0 {
		t.Fatal("a gone tag's row is deleted")
	}
	r.Seed("netflix", entries("nf-0.com")...)
	syncNow(t, h, id)
	if len(h.Events("routing.unmanaged_tags_found")) != 2 {
		t.Fatal("it came back")
	}
}

func TestRemoveAndIgnoreUnmanaged(t *testing.T) {
	h := routingtest.New(t)
	h.List("Main", h.Custom("mine", "example.org"))
	r, id := h.Router("Home", 0)
	r.Seed("netflix", entries(customNames("nf", 26)...)...)
	r.Seed("old-work", entries("work.example", "full:vpn.work.example")...)
	syncNow(t, h, id)
	if ev := h.Events("routing.unmanaged_tags_found"); len(ev) != 1 || ev[0].Payload["tags"] != "netflix, old-work" {
		t.Fatalf("found: %+v", ev)
	}

	// Remove from router: only that tag, on a sync now
	h.StartJobs()
	if _, err := h.Mod.Routers.RemoveUnmanaged(bg, id, "netflix", "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	if len(r.Names("netflix")) != 0 || len(r.Names("old-work")) != 2 || len(r.Names("mine")) != 1 {
		t.Fatalf("only netflix goes: %v", r.Tags())
	}
	s := lastSync(t, h, id)
	if s.Trigger != routers.TriggerUnmanaged || s.Removed != 1 {
		t.Fatalf("sync: %+v", s)
	}
	if ev := h.Events("routing.router_synced"); num(ev[len(ev)-1].Payload["removed"]) != 1 {
		t.Fatalf("synced: %+v", ev[len(ev)-1])
	}
	if u := unmanagedTags(t, h, id); len(u) != 1 || u["old-work"].Tag == "" {
		t.Fatalf("netflix is gone: %+v", u)
	}
	if _, err := h.Mod.Routers.RemoveUnmanaged(bg, id, "netflix", "admin"); !errors.Is(err, routers.ErrNoSuchTag) {
		t.Fatalf("no such tag: %v", err)
	}

	// Ignore: hidden from reports and notifications, left on the router
	if err := h.Mod.Routers.Ignore(bg, id, "old-work", true, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Routers.Ignore(bg, id, "old-work", true, "admin"); err != nil {
		t.Fatal(err)
	}
	ev := h.Events("routing.unmanaged_tag_ignored")
	if len(ev) != 1 || ev[0].Payload["tag"] != "old-work" || ev[0].Payload["ignored"] != true {
		t.Fatalf("ignored once: %+v", ev)
	}
	if !unmanagedTags(t, h, id)["old-work"].Ignored {
		t.Fatal("listed as ignored")
	}
	// a new tag notifies alone; the ignored one stays out of the report
	r.Seed("extra", entries("extra.example")...)
	syncNow(t, h, id)
	found := h.Events("routing.unmanaged_tags_found")
	if last := found[len(found)-1]; len(found) != 2 || last.Payload["tags"] != "extra" {
		t.Fatalf("only extra notifies: %+v", found)
	}
	if len(r.Names("old-work")) != 2 {
		t.Fatal("left on the router")
	}

	// Stop ignoring brings it back, without a notification
	syncNow(t, h, id)
	if !unmanagedTags(t, h, id)["old-work"].Ignored {
		t.Fatal("still ignored: the row stays while the tag is seen")
	}
	if err := h.Mod.Routers.Ignore(bg, id, "old-work", false, "admin"); err != nil {
		t.Fatal(err)
	}
	if ev := h.Events("routing.unmanaged_tag_ignored"); len(ev) != 2 || ev[1].Payload["ignored"] != false {
		t.Fatalf("stop ignoring: %+v", ev)
	}
	syncNow(t, h, id)
	if u := unmanagedTags(t, h, id)["old-work"]; u.Ignored || len(h.Events("routing.unmanaged_tags_found")) != 2 {
		t.Fatalf("back in the reports, no notification: %+v", u)
	}
}

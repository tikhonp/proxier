package routers_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func preview(t *testing.T, h *routingtest.Harness, id int64) routers.Sync {
	t.Helper()
	h.StartJobs()
	sid, err := h.Mod.Routers.Preview(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Settle()
	s, err := h.Mod.Routers.SyncRecord(bg, id, sid)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSwitchListPreviewAndSync(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	openai := h.Upstream("v2fly:openai")
	mine := h.Custom("mine", "mine.org")
	h.List("Main", openai, mine)
	parents := h.List("Parents", openai)
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	if err := h.Mod.Routers.SetList(bg, id, parents, "admin"); err != nil {
		t.Fatal(err)
	}
	if ev := h.Events("routing.router_updated"); len(ev) != 1 || ev[0].Payload["changes"] != "list" || ev[0].Payload["to"] != "Parents" {
		t.Fatalf("updated: %+v", ev)
	}
	p := preview(t, h, id)
	if p.State != "done" || p.Kind != "preview" || p.Removed != 1 || p.Unchanged != 1 {
		t.Fatalf("preview: %+v", p)
	}
	if p.Plan[0].Tag != "mine" || p.Plan[0].Action != routers.Remove {
		t.Fatalf("plan: %+v", p.Plan)
	}
	if !strings.Contains(p.Script, "# proxier sync · router Home · preview · file 1/1: remove mine\n") ||
		!strings.Contains(p.Script, `/ip dns static remove [find comment="mine" address-list="to_vpn_list"]`) {
		t.Fatalf("script:\n%s", p.Script)
	}
	if got, ok, _ := h.Mod.Routers.LatestPreview(bg, id); !ok || got.ID != p.ID {
		t.Fatal("the router page shows it")
	}
	// Run this sync: Sync now, which starts the delayed sync at once
	syncNow(t, h, id)
	if tags := r.Tags(); len(tags) != 1 || tags[0] != "openai" {
		t.Fatalf("mine removed: %v", tags)
	}
}

func TestPreviewIsReadOnly(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Home", 0)
	r.SeedUntagged("x.org", false)
	before := router(t, h, id)
	p := preview(t, h, id)
	if p.Added != 1 || !strings.Contains(p.Script, "# update openai (+1)") {
		t.Fatalf("preview: %+v", p)
	}
	for _, c := range r.Commands() {
		if c == "import" || c == "remove-file" || c == "remove-job-files" {
			t.Fatalf("a preview runs %s", c)
		}
	}
	if len(r.Files()) != 0 || len(r.Tags()) != 0 || len(applied(t, h, id)) != 0 {
		t.Fatal("nothing changes on the router or in Proxier")
	}
	for _, typ := range []string{"routing.router_synced", "routing.router_sync_failed", "routing.router_connected"} {
		if len(h.Events(typ)) != 0 {
			t.Fatalf("a preview records %s", typ)
		}
	}
	if after := router(t, h, id); after.Untagged != before.Untagged || !after.ReadAt.Equal(before.ReadAt) || after.LastResult != "" {
		t.Fatalf("router row: %+v", after)
	}
	// a failing preview counts nothing
	r.Offline(true)
	p = preview(t, h, id)
	if p.State != "failed" || p.Step != "connect" || !strings.Contains(p.Error, "can't reach the router") {
		t.Fatalf("failed preview: %+v", p)
	}
	if rt := router(t, h, id); rt.Failures != 0 || rt.LastResult != "" {
		t.Fatalf("counted: %+v", rt)
	}
	if len(h.Events("routing.router_sync_failed")) != 0 || len(h.Events("job.failed")) != 0 {
		t.Fatal("no event")
	}
}

package routing_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func lines(prefix string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s-%d.com\n", prefix, i)
	}
	return b.String()
}

func TestPruneSnapshotsAndGenerations(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	refresh := func(id int64) {
		t.Helper()
		if _, err := h.Mod.Refresh.Refresh(ctx, id, false, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	h.Up.V2fly("a", lines("a", 100))
	h.Up.V2fly("b", lines("b", 100))
	a, b := h.Upstream("v2fly:a"), h.Upstream("v2fly:b") // a1, b1 accepted
	h.Up.V2fly("a", lines("a", 110))
	refresh(a) // a2 accepted, a1 superseded
	h.Up.V2fly("a", lines("a", 10))
	refresh(a) // r1 rejected
	w, _, _ := h.Mod.Refresh.Waiting(ctx, a)
	if err := h.Mod.Refresh.Dismiss(ctx, a, w.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Up.V2fly("a", lines("a", 20))
	refresh(a) // r2 rejected, waiting until a3
	h.Up.V2fly("b", lines("b", 5))
	refresh(b) // rb rejected and still waiting when pruned

	h.Advance(91 * 24 * time.Hour)
	h.Up.V2fly("a", lines("a", 111))
	refresh(a) // a3 accepted, a2 superseded (old), r2 no longer waiting
	h.Up.V2fly("a", lines("a", 112))
	refresh(a) // a4 accepted, a3 superseded (recent)

	// the catalog: generation 1 in force, a stale older one and a cut-short newer one
	h.Up.V2fly("c", "c.com\n")
	h.StartJobs()
	if _, err := h.Mod.Catalog.RefreshNow(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	h.Exec(`INSERT INTO routing_catalog_entries (source, generation, kind, name) VALUES ('v2fly', 0, 'list', 'old'), ('iplist:main', 7, 'site', 'cut')`)
	h.Exec(`INSERT INTO routing_catalog_domains (source, generation, name, domain, exact) VALUES ('v2fly', 0, 'old', 'old.com', 0)`)
	h.Exec(`INSERT INTO routing_catalog_includes (generation, list, included) VALUES (0, 'old', 'c')`)

	job := h.RunSchedule("routing.prune")
	h.Drain()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("prune job: %s %s", st, e)
	}

	var kept []string
	if err := h.App.DB.R.Select(&kept, `SELECT service_id || ':' || status || ':' || suffix_count FROM routing_snapshots ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%d:accepted:100 %d:rejected:5 %d:superseded:111 %d:accepted:112", b, b, a, a)
	if strings.Join(kept, " ") != want {
		t.Errorf("kept %v, want %s", kept, want)
	}
	var stale int
	_ = h.App.DB.R.Get(&stale, `SELECT
		(SELECT count(*) FROM routing_catalog_entries e JOIN routing_catalog_sources s ON s.source = e.source WHERE e.generation <> s.generation) +
		(SELECT count(*) FROM routing_catalog_domains WHERE name = 'old') +
		(SELECT count(*) FROM routing_catalog_includes WHERE generation = 0)`)
	if stale != 0 {
		t.Errorf("%d stale catalog rows", stale)
	}
	var cur int
	_ = h.App.DB.R.Get(&cur, `SELECT count(*) FROM routing_catalog_entries WHERE name = 'c'`)
	if cur != 1 {
		t.Error("the catalog in force was pruned")
	}
	if w, ok, _ := h.Mod.Refresh.Waiting(ctx, b); !ok || w.Count() != 5 {
		t.Error("the waiting snapshot was pruned")
	}
}

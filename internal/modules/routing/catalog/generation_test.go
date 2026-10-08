package catalog_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestSearchDuringRefresh(t *testing.T) {
	h := routingtest.New(t)
	fixture(h)
	refreshCatalog(t, h)
	if g := count(t, h, `SELECT generation FROM routing_catalog_sources WHERE source = 'v2fly'`); g != 1 {
		t.Fatalf("generation %d", g)
	}

	// a refresh writing generation 2 (or cut short in the middle of it): its
	// rows are there, search doesn't see them before the flip
	h.Exec(`INSERT INTO routing_catalog_entries (source, generation, kind, name, domains) VALUES ('v2fly', 2, 'list', 'ghost', 5)`)
	h.Exec(`INSERT INTO routing_catalog_domains (source, generation, name, domain, exact) VALUES ('v2fly', 2, 'ghost', 'ghost.com', 0)`)
	h.Exec(`INSERT INTO routing_catalog_includes (generation, list, included) VALUES (2, 'ghost', 'apple')`)
	if res := search(t, h, catalog.Query{Q: "ghost"}); len(res) != 0 {
		t.Errorf("search saw an unfinished generation: %+v", res)
	}
	if res := search(t, h, catalog.Query{Q: "netflix"}); len(res) != 1 {
		t.Errorf("the old generation: %+v", res)
	}

	// the next refresh deletes the cut-short rows, writes its own and flips
	h.Up.V2fly("apple-tv", "tv.apple.com\n")
	h.Up.Commit("4444444444444444444444444444444444444444")
	refreshCatalog(t, h)
	if res := search(t, h, catalog.Query{Q: "ghost"}); len(res) != 0 {
		t.Errorf("the cut-short generation survived: %+v", res)
	}
	if res := search(t, h, catalog.Query{Q: "apple-tv"}); len(res) != 1 {
		t.Errorf("the new generation: %+v", res)
	}
	if n := count(t, h, `SELECT count(*) FROM routing_catalog_entries WHERE source = 'v2fly' AND generation <> 2`); n != 0 {
		t.Errorf("%d rows of other generations", n)
	}
	if n := count(t, h, `SELECT count(*) FROM routing_catalog_domains WHERE name = 'ghost'`) +
		count(t, h, `SELECT count(*) FROM routing_catalog_includes WHERE list = 'ghost'`); n != 0 {
		t.Errorf("%d ghost rows", n)
	}
	if n := count(t, h, `SELECT count(*) FROM routing_catalog_includes WHERE generation = 2`); n != 1 {
		t.Errorf("includes of the new generation: %d", n)
	}
}

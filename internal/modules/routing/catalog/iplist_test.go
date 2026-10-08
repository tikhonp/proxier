package catalog_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestPortalCatalogKept(t *testing.T) {
	h := routingtest.New(t)
	fixture(h)
	refreshCatalog(t, h)
	h.Up.ExportDown("beta", true)
	h.Up.Site("main", "news", "newsite.com", "newsite.com")
	h.Up.Site("beta", "news", "betanews.com", "betanews.com")
	refreshCatalog(t, h)

	if res := search(t, h, catalog.Query{Q: "newsite"}); len(res) != 1 || res[0].Portal != "main" {
		t.Errorf("main refreshed: %+v", res)
	}
	if res := search(t, h, catalog.Query{Q: "apple-dns"}); len(res) != 1 || res[0].Portal != "beta" {
		t.Errorf("beta kept: %+v", res)
	}
	if res := search(t, h, catalog.Query{Q: "betanews"}); len(res) != 0 {
		t.Errorf("beta changed: %+v", res)
	}
	st, _ := h.Mod.Catalog.Status(bg)
	for _, s := range st {
		want := 0
		if s.Source == "iplist:beta" {
			want = 1
		}
		if s.Failures != want {
			t.Errorf("%s: %d failures", s.Source, s.Failures)
		}
	}
	ev := h.Events("routing.catalog_refreshed")
	last := ev[len(ev)-1]
	if num(last.Payload["iplist_beta"]) != -1 || num(last.Payload["iplist_main"]) != 7 || num(last.Payload["iplist_russia"]) != 2 {
		t.Errorf("catalog_refreshed: %+v", last.Payload)
	}
}

package catalog_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

var bg = context.Background()

// refreshCatalog runs the catalog refresh through the job workers.
func refreshCatalog(t *testing.T, h *routingtest.Harness) {
	t.Helper()
	h.StartJobs()
	id, err := h.Mod.Catalog.RefreshNow(bg, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if st, e := h.Job(id); st != "succeeded" {
		t.Fatalf("catalog job: %s %s", st, e)
	}
}

func search(t *testing.T, h *routingtest.Harness, q catalog.Query) []catalog.Result {
	t.Helper()
	res, _, err := h.Mod.Catalog.Search(bg, q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func selectors(res []catalog.Result) []string {
	out := make([]string, 0, len(res))
	for _, r := range res {
		out = append(out, r.Selector+"/"+r.Kind)
	}
	return out
}

// fixture serves a small v2fly and three portals.
func fixture(h *routingtest.Harness) {
	h.Up.V2fly("apple", "apple.com\nicloud.com\nfull:www.apple.com @cn\nregexp:^.*\\.apple\\.com\\.cn$\n")
	h.Up.V2fly("apple-cn", "include:apple @cn\napple.cn\n")
	h.Up.V2fly("pineapple", "pineapple.org\n")
	h.Up.V2fly("netflix", "netflix.com\nnflxvideo.net\n")
	h.Up.Site("main", "apple", "apple.com", "apple.com", "icloud.com")
	h.Up.Site("main", "apple", "itunes.com", "itunes.com")
	h.Up.Site("main", "video", "youtube.com", "youtube.com", "ytimg.com")
	h.Up.Site("beta", "apple", "apple-dns.net", "apple-dns.net")
	h.Up.Site("russia", "media", "kinopoisk.ru", "kinopoisk.ru")
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// names makes n domain names "<prefix>-<i>.com", one per line.
func names(prefix string, from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "%s-%d.com\n", prefix, i)
	}
	return b.String()
}

func count(t *testing.T, h *routingtest.Harness, q string, args ...any) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, q, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

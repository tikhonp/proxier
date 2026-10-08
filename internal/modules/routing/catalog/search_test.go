package catalog_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestSearchApple(t *testing.T) {
	h := routingtest.New(t)
	fixture(h)
	refreshCatalog(t, h)

	res := search(t, h, catalog.Query{Q: "Apple"})
	want := []string{"v2fly:apple/list", "v2fly:apple-cn/list", "v2fly:pineapple/list", "iplist:apple/group", "iplist:beta:apple/group",
		"iplist:apple-dns.net/site", "iplist:apple.com/site"}
	if got := selectors(res); !slices.Equal(got, want) {
		t.Fatalf("results:\n%v\nwant\n%v", got, want)
	}
	byKey := map[string]catalog.Result{}
	for _, r := range res {
		byKey[r.Selector+"/"+r.Kind] = r
	}
	if r := byKey["v2fly:apple/list"]; r.Source != "v2fly" || r.Portal != "" || r.Domains != 3 || !r.Selectable || r.Service != nil {
		t.Errorf("v2fly:apple: %+v", r)
	}
	if r := byKey["v2fly:apple-cn/list"]; r.Domains != 2 { // apple.cn and www.apple.com through @cn
		t.Errorf("v2fly:apple-cn counts %d", r.Domains)
	}
	if r := byKey["iplist:apple/group"]; r.Portal != "main" || r.Sites != 2 || r.Domains != 3 {
		t.Errorf("group apple: %+v", r)
	}
	if r := byKey["iplist:apple.com/site"]; r.Portal != "main" || r.Group != "apple" || r.Domains != 2 {
		t.Errorf("site apple.com: %+v", r)
	}
	if r := byKey["iplist:apple-dns.net/site"]; r.Portal != "beta" || r.Source != "iplist:beta" {
		t.Errorf("site apple-dns.net: %+v", r)
	}
	if res, more, _ := h.Mod.Catalog.Search(bg, catalog.Query{Q: "apple", Limit: 2}); len(res) != 2 || more != 5 {
		t.Errorf("limit: %d more %d", len(res), more)
	}
	if res := search(t, h, catalog.Query{Q: "  "}); len(res) != 0 {
		t.Error("an empty query found something")
	}
	if v2, ip, err := h.Mod.Catalog.Matches(bg, catalog.Query{Q: "apple"}); err != nil || v2 != 3 || ip != 4 {
		t.Errorf("matches: %d %d %v", v2, ip, err)
	}
}

func TestSearchFilters(t *testing.T) {
	h := routingtest.New(t)
	fixture(h)
	refreshCatalog(t, h)
	for _, c := range []struct {
		q    catalog.Query
		want []string
	}{
		{catalog.Query{Q: "apple", Source: "iplist"}, []string{"iplist:apple/group", "iplist:beta:apple/group", "iplist:apple-dns.net/site", "iplist:apple.com/site"}},
		{catalog.Query{Q: "apple", Source: "v2fly"}, []string{"v2fly:apple/list", "v2fly:apple-cn/list", "v2fly:pineapple/list"}},
		{catalog.Query{Q: "apple", Kind: "site"}, []string{"iplist:apple-dns.net/site", "iplist:apple.com/site"}},
		{catalog.Query{Q: "apple", Kind: "group", Portal: "beta"}, []string{"iplist:beta:apple/group"}},
		{catalog.Query{Q: "a", Portal: "russia"}, []string{"iplist:media/group"}},
		{catalog.Query{Q: "kinopoisk", Source: "v2fly"}, nil},
	} {
		if got := selectors(search(t, h, c.q)); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.q, got, c.want)
		}
	}
	for _, r := range search(t, h, catalog.Query{Q: "apple", Source: "iplist"}) {
		if !strings.HasPrefix(r.Source, "iplist:") {
			t.Errorf("not iplist: %+v", r)
		}
	}
}

func TestResultAlreadyAService(t *testing.T) {
	h := routingtest.New(t)
	fixture(h)
	id := h.Upstream("v2fly:apple")
	h.List("Main", id)
	h.List("Parents")
	refreshCatalog(t, h)
	res := search(t, h, catalog.Query{Q: "apple", Source: "v2fly"})
	if len(res) == 0 || res[0].Selector != "v2fly:apple" || res[0].Service == nil || res[0].Service.ID != id ||
		!slices.Equal(res[0].Lists, []string{"Main"}) {
		t.Fatalf("v2fly:apple: %+v", res[0])
	}
	if res[1].Service != nil {
		t.Errorf("apple-cn is not a service: %+v", res[1])
	}
	// the page offers the remaining lists
	body := h.Login.Get("/routing/search?q=apple&source=v2fly").Body.String()
	for _, s := range []string{">Main<", `href="/routing/services/1/lists"`, ">Add to more lists…<",
		`href="/routing/services/add?selector=v2fly%3Aapple-cn"`, ">not a service<"} {
		if !strings.Contains(body, s) {
			t.Errorf("search page: no %q", s)
		}
	}
}

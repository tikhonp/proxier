package catalog_test

import (
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestSpelling(t *testing.T) {
	has := map[string]bool{"main/apple": true, "beta/apple": true, "beta/cloudflare.com": true, "russia/cloudflare.com": true}
	f := func(p, n string) bool { return has[p+"/"+n] }
	for _, c := range []struct{ portal, name, want string }{
		{"main", "apple", "iplist:apple"},
		{"beta", "apple", "iplist:beta:apple"},
		{"beta", "cloudflare.com", "iplist:cloudflare.com"},
		{"russia", "cloudflare.com", "iplist:russia:cloudflare.com"},
		{"russia", "kinopoisk.ru", "iplist:kinopoisk.ru"},
	} {
		if got := catalog.Spell(c.portal, c.name, f); got != c.want {
			t.Errorf("Spell(%s, %s) = %s, want %s", c.portal, c.name, got, c.want)
		}
	}

	h := routingtest.New(t)
	h.Up.Site("main", "apple", "apple.com", "apple.com")
	h.Up.Site("beta", "apple", "icloud.com", "icloud.com")
	// on russia a site and a group are both called "music": the site is found first
	h.Up.Site("russia", "music", "music", "music.yandex.ru")
	h.Up.Site("russia", "music", "zvuk.com", "zvuk.com")
	refreshCatalog(t, h)
	res := search(t, h, catalog.Query{Q: "apple", Kind: "group"})
	if got := selectors(res); !slices.Equal(got, []string{"iplist:apple/group", "iplist:beta:apple/group"}) {
		t.Errorf("apple groups: %v", got)
	}
	for _, r := range search(t, h, catalog.Query{Q: "music"}) {
		if r.Kind == "group" && (r.Selectable || r.Service != nil) {
			t.Errorf("a shadowed group is selectable: %+v", r)
		}
		if r.Kind == "site" && (!r.Selectable || r.Selector != "iplist:music") {
			t.Errorf("the site: %+v", r)
		}
	}
	body := h.Login.Get("/routing/search?q=music&kind=group").Body.String()
	if !contains(body, "a site of this name is found first") || contains(body, "add?selector=iplist%3Amusic") {
		t.Error("the page offers a shadowed group")
	}
}

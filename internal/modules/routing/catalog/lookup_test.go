package catalog_test

import (
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestLookupMatches(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\nfull:api.claude.ai\n")
	h.Up.V2fly("claude-web", "full:www.claude.ai\n")
	h.Up.V2fly("category-ai-!cn", "include:anthropic\ninclude:claude-web\n")
	h.Up.V2fly("geolocation-!cn", "include:category-ai-!cn\n")
	h.Up.V2fly("category-ads", "include:anthropic @ads\n")    // claude.ai has no @ads
	h.Up.V2fly("ai-not-cn", "include:anthropic @-cn\n")       // and no @cn: taken
	h.Up.V2fly("github", "github.io\nfull:pages.github.io\n") // above me.github.io
	h.Up.Site("main", "ai", "claude.ai", "claude.ai", "anthropic.com")
	h.Up.Site("main", "dev", "github.com", "github.com")
	refreshCatalog(t, h)
	svc := h.Upstream("v2fly:anthropic")

	got, err := h.Mod.Catalog.Lookup(bg, "www.claude.ai", "claude.ai")
	if err != nil {
		t.Fatal(err)
	}
	var sels []string
	for _, s := range got {
		sels = append(sels, s.Selector+"/"+s.Match)
	}
	want := []string{"v2fly:claude-web/exact", "v2fly:anthropic/suffix", "iplist:claude.ai/suffix"}
	if !slices.Equal(sels, want) {
		t.Fatalf("suggestions %v, want %v", sels, want)
	}
	a := got[1]
	if a.Name != "claude.ai" || a.Domains != 3 || a.Kind != "list" || a.Service == nil || a.Service.ID != svc ||
		!slices.Equal(a.Via, []string{"ai-not-cn", "category-ai-!cn", "geolocation-!cn"}) {
		t.Errorf("anthropic: %+v", a)
	}
	if w := got[0]; w.Name != "www.claude.ai" || w.Service != nil || !slices.Equal(w.Via, []string{"category-ai-!cn", "geolocation-!cn"}) {
		t.Errorf("claude-web: %+v", w)
	}
	if s := got[2]; s.Kind != "site" || s.Portal != "main" || s.Domains != 2 || s.Name != "claude.ai" {
		t.Errorf("iplist site: %+v", s)
	}

	// the registrable domain's parent as a suffix is "above"; its exact row holds nothing of the site
	got, err = h.Mod.Catalog.Lookup(bg, "me.github.io", "me.github.io")
	if err != nil || len(got) != 1 || got[0].Selector != "v2fly:github" || got[0].Match != catalog.MatchAbove || got[0].Name != "github.io" {
		t.Fatalf("me.github.io: %+v %v", got, err)
	}
	if got, err := h.Mod.Catalog.Lookup(bg, "example.org", "example.org"); err != nil || len(got) != 0 {
		t.Errorf("nothing holds example.org: %+v %v", got, err)
	}
}

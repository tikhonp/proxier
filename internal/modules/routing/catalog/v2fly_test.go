package catalog_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestV2flyCatalogKeptWhenGitHubDown(t *testing.T) {
	h := routingtest.New(t)
	fixture(h)
	if err := h.App.Settings.Set(bg, "admin", "routing", map[string]string{"routing.github_token": "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	refreshCatalog(t, h)
	if h.Up.Requests("/codeload/") != 1 || h.Up.Auth() != "Bearer ghp_secret" {
		t.Fatalf("first refresh: %d downloads, auth %q", h.Up.Requests("/codeload/"), h.Up.Auth())
	}
	refreshed := len(h.Events("routing.catalog_refreshed"))
	if refreshed != 1 {
		t.Fatalf("catalog_refreshed: %d", refreshed)
	}

	// an unchanged commit downloads nothing and records nothing
	h.Advance(24 * time.Hour)
	refreshCatalog(t, h)
	if h.Up.Requests("/codeload/") != 1 || len(h.Events("routing.catalog_refreshed")) != 1 {
		t.Errorf("unchanged: %d downloads, %d events", h.Up.Requests("/codeload/"), len(h.Events("routing.catalog_refreshed")))
	}

	h.Up.GitHubDown(true)
	for day := 1; day <= 4; day++ {
		h.Advance(24 * time.Hour)
		refreshCatalog(t, h)
		want := 0
		if day >= 3 {
			want = 1
		}
		if n := len(h.Events("routing.catalog_refresh_failed")); n != want {
			t.Errorf("day %d: %d catalog_refresh_failed", day, n)
		}
	}
	ev := h.Events("routing.catalog_refreshed")
	if last := ev[len(ev)-1]; num(last.Payload["v2fly"]) != -1 || num(last.Payload["iplist_main"]) <= 0 {
		t.Errorf("catalog_refreshed: %+v", last.Payload)
	}
	f := h.Events("routing.catalog_refresh_failed")
	if f[0].Payload["source"] != "v2fly" || f[0].Payload["error"] != "HTTP 502" || f[0].Payload["since"] != "2026-10-10T12:00:00.000Z" ||
		f[0].Subject.String() != "routing:catalog" {
		t.Errorf("catalog_refresh_failed: %+v", f[0])
	}
	// kept, and aged: the last good refresh was four days ago
	res := search(t, h, catalog.Query{Q: "apple", Source: "v2fly"})
	if len(res) != 3 || res[0].Age != 4*24*time.Hour {
		t.Fatalf("kept: %+v", res)
	}
	h.Advance(-24 * time.Hour) // three days after the last good refresh
	body := h.Login.Get("/routing/search?q=apple&source=v2fly").Body.String()
	if !contains(body, "catalog 3 days old") || !contains(body, "v2fly: failing since") {
		t.Error("the page doesn't say the catalog is 3 days old and failing")
	}
	st, _ := h.Mod.Catalog.Status(bg)
	if st[0].Source != "v2fly" || st[0].Failures != 4 || st[0].LastError != "HTTP 502" {
		t.Errorf("status: %+v", st[0])
	}

	// back: the failures end
	h.Up.GitHubDown(false)
	h.Up.Commit("2222222222222222222222222222222222222222")
	downloads := h.Up.Requests("/codeload/")
	refreshCatalog(t, h)
	st, _ = h.Mod.Catalog.Status(bg)
	if st[0].Failures != 0 || h.Up.Requests("/codeload/") != downloads+1 {
		t.Errorf("recovered: %+v", st[0])
	}
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

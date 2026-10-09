package pages_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestSearchPage(t *testing.T) {
	h := routingtest.New(t)
	body := h.Login.Get("/routing/search").Body.String()
	has(t, "never refreshed", body, "Search the catalog", "The catalog fills every day at 04:30, or now with Refresh catalog.",
		">Refresh catalog<", `action="/routing/catalog/refresh"`, `hx-trigger="input changed delay:300ms`, `name="q"`,
		"Type part of a name", `name="kind"`, `name="portal"`)

	h.Up.V2fly("apple", "apple.com\nicloud.com\n")
	h.Up.V2fly("pineapple", "pineapple.org\n")
	for i := 0; i < 205; i++ {
		h.Up.V2fly(fmt.Sprintf("many-%03d", i), "many.com\n")
	}
	h.Up.Site("main", "apple", "apple.com", "apple.com")
	h.Up.Site("beta", "apple", "icloud.com", "icloud.com")
	rec := h.Login.Post("/routing/catalog/refresh", url.Values{"q": {"apple"}})
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/routing/search?") || !strings.Contains(loc, "q=apple") {
		t.Fatalf("Refresh catalog: %d %s", rec.Code, loc)
	}
	has(t, "queued", h.Login.Get(loc).Body.String(), "Catalog refresh queued · job #")
	h.StartJobs()
	h.Drain()

	body = h.Login.Get("/routing/search?q=apple").Body.String()
	has(t, "results", body, "refreshed 2026-10-08 15:00", "All sources", "v2fly · 2", "iplist · 3",
		">v2fly:apple<", "list · 2 domains", ">iplist:apple<", "group · 1 site · 1 domains", ">iplist:beta:apple<", "beta, added pinned",
		">iplist:apple.com<", "site · group apple", ">not a service<", `data-row="v2fly:apple"`,
		`href="/routing/services/add?selector=v2fly%3Aapple"`, ">Add to lists…<", `data-key="a"`, ">Preview<",
		`hx-get="/routing/search/preview?selector=v2fly%3Aapple"`, `data-href="/routing/search?q=apple&amp;selector=v2fly%3Aapple"`)
	if strings.Contains(body, "The catalog fills") {
		t.Error("the empty-catalog line after a refresh")
	}
	if i, j := strings.Index(body, ">v2fly:apple<"), strings.Index(body, ">iplist:apple<"); i < 0 || j < i {
		t.Error("v2fly lists come first")
	}
	// the chips and filters stay in the URL
	body = h.Login.Get("/routing/search?q=apple&source=iplist&kind=group").Body.String()
	has(t, "filtered", body, `aria-pressed="true"`, `<option value="group" selected>`, ">iplist:beta:apple<")
	if strings.Contains(body, ">v2fly:apple<") || strings.Contains(body, ">iplist:apple.com<") {
		t.Error("the filters let other rows through")
	}
	// the query field's htmx request gets the same page; it swaps #search-results
	r := h.Site.Do(sitetest.Req{Method: "GET", Path: "/routing/search?q=pine", Cookies: []*http.Cookie{h.Login.Cookie},
		Header: http.Header{"Hx-Request": {"true"}}})
	has(t, "htmx", r.Body.String(), `id="search-results"`, ">v2fly:pineapple<")

	has(t, "more", h.Login.Get("/routing/search?q=many").Body.String(), "5 more: type more of the name")
	has(t, "nothing", h.Login.Get("/routing/search?q=kinopoisk").Body.String(), "Nothing in the catalog matches “kinopoisk”.")

	// already a service: in its lists, Open service once every list has it
	id := h.Upstream("v2fly:pineapple")
	h.List("Main", id)
	has(t, "a service", h.Login.Get("/routing/search?q=pineapple").Body.String(), ">Main<", fmt.Sprintf(`href="/routing/services/%d"`, id), ">Open service<")
}

func TestPreview(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\nfull:api.openai.com\nregexp:^.*\\.openai\\.com\\.cn$\nkeyword:openai\n")
	body := hx(h, "GET", "/routing/search/preview?selector=v2fly:openai", nil)
	has(t, "preview", body, ">v2fly:openai<", "fetched just now · not a service yet", "2 suffix", "1 exact", "2 skipped",
		">chatgpt.com<", ">api.openai.com<", ">exact<", "regexp:^.*\\.openai\\.com\\.cn$", "not supported on RouterOS", "keyword:openai",
		`href="/routing/services/add?selector=v2fly%3Aopenai"`, "creates the service, tag openai", "data-esc")

	has(t, "missing", hx(h, "GET", "/routing/search/preview?selector=v2fly:nope", nil), "v2fly has no list", `role="alert"`)
	if b := hx(h, "GET", "/routing/search/preview", nil); strings.TrimSpace(b) != "" {
		t.Errorf("an empty selector: %q", b)
	}

	// without htmx it is the search page with the drawer
	rec := h.Login.Get("/routing/search/preview?selector=v2fly:openai")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/routing/search?selector=v2fly%3Aopenai" {
		t.Fatalf("no htmx: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	has(t, "full page", h.Login.Get(rec.Header().Get("Location")).Body.String(), "Search the catalog", `id="preview"`, "2 suffix")

	// a service already: named, Open service
	id := h.Upstream("v2fly:openai")
	h.List("Main", id)
	has(t, "service", hx(h, "GET", "/routing/search/preview?selector=v2fly:openai", nil), "service openai · in Main", ">Open service<")

	// a selector with no usable name still shows what was skipped
	h.Up.V2fly("weird", "regexp:^x$\n")
	has(t, "empty", hx(h, "GET", "/routing/search/preview?selector=weird", nil), "regexp:^x$", "has no names RouterOS can use")
}

func TestSearchSuggestsDiscover(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("apple", "apple.com\n")
	h.StartJobs()
	if _, err := h.Mod.Catalog.RefreshNow(bg(), "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	body := h.Login.Get("/routing/search?q=kinopoisk").Body.String()
	has(t, "dotless", body, "Nothing in the catalog matches", `href="/routing/discover?website=kinopoisk.com"`, "Discover kinopoisk.com…",
		"opens a browser and lists every domain the site loads")
	body = h.Login.Get("/routing/search?q=kinopoisk.ru").Body.String()
	has(t, "with a dot", body, `href="/routing/discover?website=kinopoisk.ru"`, "Discover kinopoisk.ru…")
	if body := h.Login.Get("/routing/search?q=apple").Body.String(); strings.Contains(body, "/routing/discover?website=") {
		t.Error("a search with results suggests discovery")
	}
	// the link opens the form prefilled
	has(t, "form", h.Login.Get("/routing/discover?website=kinopoisk.com").Body.String(), `value="kinopoisk.com"`)
}

package pages_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"golang.org/x/net/html"
)

func has(t *testing.T, what, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Errorf("%s: no %q", what, s)
		}
	}
}

// hx is an htmx request of the signed-in admin.
func hx(h *routingtest.Harness, method, path string, form url.Values) string {
	h.T.Helper()
	r := sitetest.Req{Method: method, Path: path, Cookies: []*http.Cookie{h.Login.Cookie}, Header: http.Header{"Hx-Request": {"true"}}}
	if form != nil {
		form.Set("_csrf", h.Login.CSRF)
		r.Form = form
	}
	rec := h.Site.Do(r)
	if rec.Code != 200 {
		h.T.Fatalf("%s %s: %d", method, path, rec.Code)
	}
	return rec.Body.String()
}

func TestServicesList(t *testing.T) {
	h := routingtest.New(t)
	body := h.Login.Get("/routing/services").Body.String()
	has(t, "empty", body, "A service is a named set of domains from one source", `href="/routing/services/add"`, `href="/routing/services/new"`)

	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\nfull:api.anthropic.com\n")
	h.Up.Site("main", "video", "youtube.com", "youtube.com")
	h.Upstream("anthropic")
	h.Upstream("iplist:youtube.com")
	h.Custom("mine", "example.com")
	body = h.Login.Get("/routing/services").Body.String()
	has(t, "list", body, "3 services · 1 custom", ">anthropic<", "v2fly:anthropic", ">v2fly<", "2 + 1", "added 8 Oct", ">ok<",
		">mine<", ">custom<", "saved 8 Oct", ">youtube.com<", "iplist:youtube.com", "1–3 of 3", `aria-pressed="true"`)
	if i, j := strings.Index(body, ">anthropic<"), strings.Index(body, ">youtube.com<"); i < 0 || j < i {
		t.Error("not sorted by tag")
	}
	body = h.Login.Get("/routing/services?source=custom").Body.String()
	if !strings.Contains(body, ">mine<") || strings.Contains(body, ">anthropic<") {
		t.Error("the source filter")
	}
	body = h.Login.Get("/routing/services?source=url").Body.String()
	has(t, "nothing matches", body, "No service matches these filters.")

	for i := 0; i < 50; i++ {
		h.Custom(fmt.Sprintf("c%02d", i))
	}
	body = h.Login.Get("/routing/services").Body.String()
	has(t, "page 1", body, "1–50 of 53", `href="/routing/services?page=2"`)
	body = h.Login.Get("/routing/services?page=2").Body.String()
	has(t, "page 2", body, "51–53 of 53", ">youtube.com<")
}

func TestAddServicePage(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	body := h.Login.Get("/routing/services/add").Body.String()
	has(t, "form", body, `placeholder="v2fly:anthropic"`, "a bare name means v2fly:")

	for sel, want := range map[string]string{
		"":                             "Type a selector.",
		"foo:bar":                      "Unknown source &#39;foo&#39;",
		"iplist:alpha:apple":           "iplist has no portal &#39;alpha&#39;",
		"ftp://x/y":                    "Only http:// and https:// addresses",
		"https://x/.txt":               "Can&#39;t make a tag from this URL",
		"v2fly:nosuchlist":             "v2fly has no list &#39;nosuchlist&#39;.",
		h.Up.URL() + "/files/gone.txt": "/files/gone.txt: HTTP 404",
	} {
		rec := h.Login.Post("/routing/services", url.Values{"selector": {sel}})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%q: %d, want %q", sel, rec.Code, want)
		}
	}

	rec := h.Login.Post("/routing/services", url.Values{"selector": {"anthropic"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/routing/services/1?added=1" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	has(t, "the band", h.Login.Get("/routing/services/1?added=1").Body.String(), "Added anthropic: 2 names.")

	h.Up.Site("main", "ai", "anthropic", "anthropic.com")
	rec = h.Login.Post("/routing/services", url.Values{"selector": {"iplist:anthropic"}})
	has(t, "taken", rec.Body.String(), "anthropic already exists", "v2fly:anthropic", "in no list",
		`action="/routing/services/1/switch"`, `value="iplist:anthropic"`, "Switch anthropic to iplist:anthropic")
	if rec.Code != 200 {
		t.Errorf("taken: %d", rec.Code)
	}
	rec = h.Login.Post("/routing/services/1/switch", url.Values{"selector": {"iplist:anthropic"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("switch: %d", rec.Code)
	}
	it, _ := h.Mod.Services.Get(context.Background(), 1)
	if it.Selector != "iplist:anthropic" {
		t.Errorf("switch: %s", it.Selector)
	}
	rec = h.Login.Post("/routing/services/1/switch", url.Values{"selector": {"iplist:claude.ai"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "A switch keeps the tag: iplist:claude.ai has tag claude.ai, not anthropic.") {
		t.Errorf("switch to another tag: %d", rec.Code)
	}
}

func TestServicePage(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	var b strings.Builder
	b.WriteString("netflix.com\nfull:api.netflix.com\nfull:www.netflix.com\nregexp:^x$\nkeyword:nflx\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "n%02d.nflxvideo.net\n", i)
	}
	h.Up.V2fly("netflix", b.String())
	id := h.Upstream("netflix")
	body := h.Login.Get("/routing/services/1").Body.String()
	has(t, "page", body, "<h1>netflix</h1>", "v2fly:netflix · tag netflix · in no list", "63 · 61 suffix, 2 exact · 2 skipped",
		"api.netflix.com", "under netflix.com", "1–50 of 63", "regexp:^x$", "not supported on RouterOS",
		"Selector", "Last accepted", `href="/routing/services/1/switch"`, `href="/routing/services/1/remove"`,
		"Added service netflix (v2fly)", `href="/routing/services/1/snapshots/1"`, ">accepted<")
	if strings.Contains(body, `href="/routing/services/1/edit"`) {
		t.Error("an upstream service offers Edit domains")
	}

	frag := hx(h, "GET", "/routing/services/1/domains?q=API", nil)
	if !strings.Contains(frag, "api.netflix.com") || strings.Contains(frag, "n05.nflxvideo.net") || strings.Contains(frag, "<html") {
		t.Errorf("the filter: %s", frag)
	}
	frag = hx(h, "GET", "/routing/services/1/domains?page=2", nil)
	has(t, "page 2", frag, "51–63 of 63", "www.netflix.com")
	if rec := h.Login.Get("/routing/services/1/domains?q=api"); rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != "/routing/services/1?q=api#domains" {
		t.Errorf("without htmx: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	has(t, "filtered page", h.Login.Get("/routing/services/1?q=api").Body.String(), "1–1 of 1")

	// a switch makes a second snapshot; its diff
	h.Up.File("netflix.txt", "netflix.com\nnew.netflix.com\n")
	if err := h.Mod.Services.Switch(ctx, id, "netflix="+h.Up.URL()+"/files/netflix.txt", "admin"); err != nil {
		t.Fatal(err)
	}
	body = h.Login.Get("/routing/services/1").Body.String()
	has(t, "history", body, `href="/routing/services/1/snapshots/2"`, ">superseded<", "+1 −62", "Switched source from v2fly:netflix to netflix=")
	diff := h.Login.Get("/routing/services/1/snapshots/2").Body.String()
	has(t, "diff", diff, "Added · 1", "Removed · 62", "api.netflix.com", "n00.nflxvideo.net", "against the snapshot of")
	first := h.Login.Get("/routing/services/1/snapshots/1").Body.String()
	has(t, "the first diff", first, "Added · 63", "the first snapshot")
	if rec := h.Login.Get("/routing/services/1/snapshots/99"); rec.Code != 404 {
		t.Errorf("a missing snapshot: %d", rec.Code)
	}

	// a custom service offers Edit domains; remove
	cid := h.Custom("mine", "example.com")
	body = h.Login.Get(fmt.Sprintf("/routing/services/%d", cid)).Body.String()
	has(t, "custom", body, "custom · tag mine · in no list", fmt.Sprintf(`href="/routing/services/%d/edit"`, cid), "Saved domains (+1 −0)")
	h.Exec(`INSERT INTO routing_list_services (list_id, service_id, position, added_at) VALUES (1, ?, 1, '2026-10-08T12:00:00.000Z')`, cid)
	rm := h.Login.Get(fmt.Sprintf("/routing/services/%d/remove", cid)).Body.String()
	has(t, "held", rm, "Take it out of Main first", "disabled")
	if rec := h.Login.Post(fmt.Sprintf("/routing/services/%d/remove", cid), nil); rec.Code != http.StatusConflict {
		t.Errorf("remove in Main: %d", rec.Code)
	}
	has(t, "remove", h.Login.Get("/routing/services/1/remove").Body.String(), "Its snapshot history goes too. It is in no routing list")
	if rec := h.Login.Post("/routing/services/1/remove", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/routing/services" {
		t.Errorf("remove: %d", rec.Code)
	}
	if rec := h.Login.Get("/routing/services/1"); rec.Code != 404 {
		t.Errorf("removed: %d", rec.Code)
	}
}

func editForm(rows ...string) url.Values {
	v := url.Values{"name": {"mine"}, "tag": {"mine"}, "description": {""}, "rows": {fmt.Sprint(len(rows))}}
	for i, r := range rows {
		k := fmt.Sprint(i + 1)
		match := "suffix"
		if strings.HasPrefix(r, "=") {
			r, match = r[1:], "exact"
		}
		v.Set("domain."+k, r)
		v.Set("match."+k, match)
		v.Set("note."+k, "")
	}
	return v
}

func TestEditor(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	id := h.Custom("mine", "example.com")
	path := fmt.Sprintf("/routing/services/%d/edit", id)
	body := h.Login.Get(path).Body.String()
	has(t, "editor", body, "Domains · 1", `value="example.com"`, "Paste many", "Save 1 domain", `name="rows" value="1"`)

	// Preview: the report, the table unchanged
	f := editForm("example.com")
	f.Set("paste", "https://www.kinopoisk.ru/film/1\n*.hd.kinopoisk.ru\nfull:api.ott.kinopoisk.ru\nkinopoisk.ru\nКинопоиск.рф\n93.158.134.11\napi.example.com")
	f.Set("op", "preview")
	rec := h.Login.Post(path, f)
	body = rec.Body.String()
	has(t, "preview", body, "+2 added · 4 merged · 1 refused", "merged: www.kinopoisk.ru goes under kinopoisk.ru",
		"merged: api.example.com goes under example.com", "punycode: кинопоиск.рф → xn--h1aaecngahu.xn--p1ai",
		"refused: 93.158.134.11 is an IP address", "Add 2 to the table", "Domains · 1", `name="rows" value="1"`)
	if rec.Code != 200 || !strings.Contains(body, "https://www.kinopoisk.ru/film/1") {
		t.Errorf("the paste was not kept: %d", rec.Code)
	}

	// Add to the table: the rows with the + marker, the paste cleared; htmx swaps the form only
	f.Set("op", "add")
	body = hx(h, "POST", path, f)
	has(t, "added", body, "Domains · 3", `name="rows" value="3"`, `value="kinopoisk.ru"`, `value="xn--h1aaecngahu.xn--p1ai"`, "кинопоиск.рф")
	if strings.Contains(body, "<html") || strings.Contains(body, "https://www.kinopoisk.ru/film/1") || strings.Count(body, `<span class="mark" aria-hidden="true">+</span>`) != 2 {
		t.Errorf("the htmx answer: %d new rows", strings.Count(body, `<span class="mark" aria-hidden="true">+</span>`))
	}
	if rows, _ := h.Mod.Services.CustomRows(ctx, id); len(rows) != 1 {
		t.Error("Add to the table saved")
	}

	// × removes a row
	f = editForm("example.com", "one.org", "two.org")
	f.Set("op", "remove-2")
	body = h.Login.Post(path, f).Body.String()
	has(t, "removed", body, "Domains · 2", `value="two.org"`)
	if strings.Contains(body, `value="one.org"`) {
		t.Error("× kept the row")
	}

	// errors in place, nothing saved
	f = editForm("example.com", "1.2.3.4", "fine.org")
	f.Set("op", "save")
	rec = h.Login.Post(path, f)
	body = rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("errors: %d", rec.Code)
	}
	has(t, "errors", body, "Nothing was saved", "is an IP address; services hold domains only", `value="1.2.3.4"`, `value="fine.org"`)
	if body := hx(h, "POST", path, f); !strings.Contains(body, "is an IP address") {
		t.Error("htmx errors answer 200 with the form")
	}
	if rows, _ := h.Mod.Services.CustomRows(ctx, id); len(rows) != 1 {
		t.Error("a refused save wrote")
	}

	// Save (also with no op: ⌘↵)
	f = editForm("example.com", "=api.other.org", "other.org")
	f.Set("note.3", "the other one")
	rec = h.Login.Post(path, f)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/routing/services/%d?saved=1&merged=1", id) {
		t.Fatalf("save: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rows, _ := h.Mod.Services.CustomRows(ctx, id)
	if len(rows) != 2 || rows[1] != (services.DomainRow{Domain: "other.org", Note: "the other one"}) {
		t.Errorf("saved: %+v", rows)
	}
	// the band says api.other.org went under other.org
	has(t, "saved page", h.Login.Get(rec.Header().Get("Location")).Body.String(), "Saved mine: 2 names. 1 row was merged into another.", "the other one")
	if rec := h.Login.Get("/routing/services/999/edit"); rec.Code != 404 {
		t.Errorf("missing: %d", rec.Code)
	}
}

func checkNoInline(t *testing.T, name, page string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "style" {
				t.Errorf("%s: <style> element", name)
			}
			if n.Data == "script" {
				src := false
				for _, a := range n.Attr {
					src = src || a.Key == "src"
				}
				if !src {
					t.Errorf("%s: inline <script>", name)
				}
			}
			for _, a := range n.Attr {
				if a.Key == "style" || strings.HasPrefix(a.Key, "on") || strings.HasPrefix(a.Key, "hx-on") ||
					((a.Key == "href" || a.Key == "src") && strings.HasPrefix(strings.ToLower(a.Val), "javascript:")) {
					t.Errorf("%s: <%s %s=…>", name, n.Data, a.Key)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

func TestNoInlineStyleOrScript(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nregexp:x\n")
	h.Upstream("anthropic")
	h.Custom("mine", "example.com", "пример.рф")
	f := editForm("example.com")
	f.Set("paste", "1.2.3.4\nb.com")
	f.Set("op", "preview")
	pages := map[string]string{
		"list":    h.Login.Get("/routing/services").Body.String(),
		"add":     h.Login.Get("/routing/services/add").Body.String(),
		"taken":   h.Login.Post("/routing/services", url.Values{"selector": {"anthropic"}}).Body.String(),
		"new":     h.Login.Get("/routing/services/new").Body.String(),
		"service": h.Login.Get("/routing/services/1").Body.String(),
		"custom":  h.Login.Get("/routing/services/2").Body.String(),
		"editor":  h.Login.Get("/routing/services/2/edit").Body.String(),
		"preview": h.Login.Post("/routing/services/2/edit", f).Body.String(),
		"switch":  h.Login.Get("/routing/services/1/switch").Body.String(),
		"diff":    h.Login.Get("/routing/services/1/snapshots/1").Body.String(),
		"remove":  h.Login.Get("/routing/services/1/remove").Body.String(),
		"search":  h.Login.Get("/routing/search?q=anthropic&selector=v2fly:anthropic").Body.String(),
		"drawer":  "<div>" + hx(h, "GET", "/routing/search/preview?selector=v2fly:anthropic", nil) + "</div>",
		"routing": h.Login.Get("/settings/routing").Body.String(),
	}
	h.Up.V2fly("anthropic", "")
	if _, err := h.Mod.Refresh.Refresh(bg(), 1, false, "admin"); err != nil {
		t.Fatal(err)
	}
	pages["rejected"] = h.Login.Get("/routing/services/1").Body.String()
	if !strings.Contains(pages["rejected"], "was held back") {
		t.Error("no rejected band to check")
	}
	for name, body := range pages {
		if len(body) < 50 {
			t.Errorf("%s: empty page", name)
		}
		checkNoInline(t, name, body)
	}
}

func TestStatesOnListPages(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("a", "a.com\n")
	h.Up.V2fly("b", "b.com\n")
	h.Up.V2fly("netflix", nfNames(0, 212))
	a, b, nf := h.Upstream("v2fly:a"), h.Upstream("v2fly:b"), h.Upstream("v2fly:netflix")
	mine := h.Custom("mine", "mine.com")
	main := h.List("Main", a, b, nf, mine)
	h.Up.V2fly("netflix", nfNames(129, 212))
	h.Up.Fail("/v2fly/data/b", http.StatusBadGateway)
	for _, id := range []int64{a, b, nf} {
		if _, err := h.Mod.Refresh.Refresh(bg(), id, false, "admin"); err != nil {
			t.Fatal(err)
		}
	}

	body := h.Login.Get("/routing/services").Body.String()
	has(t, "list", body, "4 services · 1 custom · refreshed daily at 04:00 · 1 snapshot waiting · 1 failing",
		">waiting<", ">failing<", ">ok<", "ok · 2", "waiting · 1", "failing · 1", `href="/routing/services?state=waiting"`,
		`data-key="r"`, `name="from" value="list"`)
	if strings.Count(body, `data-key="r"`) != 3 {
		t.Errorf("%d refresh buttons, want 3 (no custom)", strings.Count(body, `data-key="r"`))
	}
	body = h.Login.Get("/routing/services?state=waiting").Body.String()
	if !strings.Contains(body, ">netflix<") || strings.Contains(body, ">mine<") || strings.Contains(body, `"/routing/services/1"`) {
		t.Error("the state filter")
	}
	has(t, "pressed", body, `href="/routing/services"`)

	rec := h.Login.Post(fmt.Sprintf("/routing/services/%d/refresh", a), url.Values{"from": {"list"}})
	if rec.Header().Get("Location") != "/routing/services?refreshed=a" {
		t.Fatalf("row refresh: %s", rec.Header().Get("Location"))
	}
	has(t, "row band", h.Login.Get("/routing/services?refreshed=a").Body.String(), "Refresh of a queued.")

	// the list page: states, warnings, Refresh all
	lh := fmt.Sprintf("/routing/lists/%d", main)
	body = h.Login.Get(lh).Body.String()
	has(t, "list page", body,
		"Rejected snapshot · v2fly:netflix: a refresh lost 60 % of its domains (212 → 83). Targets keep the old 212 until you decide.",
		"Refresh failing · v2fly:b: 1 failure in a row, HTTP 502", fmt.Sprintf(`href="/routing/services/%d"`, nf),
		">waiting<", ">failing<", ">Refresh all<", `action="`+lh+`/refresh"`)
	var queued int
	_ = h.App.DB.R.Get(&queued, `SELECT count(*) FROM jobs WHERE type = 'routing.refresh'`)
	rec = h.Login.Post(lh+"/refresh", url.Values{})
	if rec.Header().Get("Location") != lh+"?refreshing=3" {
		t.Fatalf("Refresh all: %s", rec.Header().Get("Location"))
	}
	var n int
	_ = h.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE type = 'routing.refresh'`)
	if n-queued != 2 { // a's own refresh was already queued: it coalesced
		t.Errorf("Refresh all queued %d new jobs", n-queued)
	}
	var keys []string
	_ = h.App.DB.R.Select(&keys, `SELECT resource_key FROM jobs WHERE type = 'routing.refresh' ORDER BY resource_key`)
	if strings.Join(keys, ",") != fmt.Sprintf("service:%d,service:%d,service:%d", a, b, nf) {
		t.Errorf("jobs: %v", keys)
	}
	has(t, "refreshing band", h.Login.Get(lh+"?refreshing=3").Body.String(), "Refreshing 3 services.")
}

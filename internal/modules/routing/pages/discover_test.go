package pages_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery/discoverytest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestDiscoverPages(t *testing.T) {
	b := discoverytest.New()
	h := routingtest.New(t, routingtest.WithBrowser(b))
	h.Servers.Put(1, "nl-1")
	h.Servers.Put(2, "de-1")
	h.Servers.SetHealth(2, "blocked")
	h.StartJobs()
	h.Up.V2fly("example", "example.com\n")
	if _, err := h.Mod.Catalog.RefreshNow(bg(), "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	ex := h.Custom("ex", "example.com")
	h.List("Main", ex)

	form := h.Login.Get("/routing/discover?website=claude.ai").Body.String()
	has(t, "form", form, `value="claude.ai"`, `name="via" value="direct" checked`, `value="auto"`, ">through nl-1<",
		`value="server:2" disabled`, "blocked: can&#39;t be chosen", "Chromium: connected · HeadlessChrome/131", `name="depth" value="5"`,
		`data-action="discovery.start"`)
	rec := h.Login.Post("/routing/discover", url.Values{"website": {"203.0.113.5"}, "via": {"direct"}, "depth": {"0"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Type a website&#39;s name, like claude.ai.") {
		t.Fatalf("a bad website: %d", rec.Code)
	}

	page := "https://www.example.com/"
	res := discovery.Result{Title: "Example Domain", Pages: []discovery.Page{
		{URL: page, Loaded: true, Status: 200, Screenshot: []byte{0xff, 0xd8, 0xff, 0xe0}},
	}, Requests: []discovery.Request{
		{URL: page, Host: "www.example.com", Type: "Document", Status: 200},
		{URL: "https://static.example.com/a.js", Host: "static.example.com", Type: "Script", Failed: "net::ERR_CONNECTION_RESET"},
		{URL: "https://www.google-analytics.com/g.js", Host: "www.google-analytics.com", Type: "Script", Status: 200},
		{URL: "https://203.0.113.5/p", Host: "203.0.113.5", Type: "Image", Status: 200},
	}}
	b.Site(page, "", res)
	release := b.Block()
	defer release()
	rec = h.Login.Post("/routing/discover", url.Values{"website": {"www.example.com"}, "via": {"direct"}, "depth": {"0"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	run := rec.Header().Get("Location")
	if !b.Entered(10 * time.Second) {
		t.Fatal("no visit")
	}
	body := h.Login.Get(run).Body.String()
	has(t, "running", body, `hx-trigger="every 2s"`, run+"/status", ">v2fly:example<", "contains example.com as a suffix", "Visiting…",
		"Discover</a>", "run #", "registrable domain example.com")
	release()
	deadline := time.Now().Add(10 * time.Second)
	for strings.Contains(body, "every 2s") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		body = hx(h, "GET", run+"/status", nil)
	}
	has(t, "status fragment", body, `id="run-areas"`, `id="run-state" hx-swap-oob="true"`, "finished")
	body = h.Login.Get(run).Body.String()
	if strings.Contains(body, "every 2s") {
		t.Error("a finished run still polls")
	}
	has(t, "finished", body, "finished", "Example Domain", run+"/screenshots/1-1.jpg", "loaded · 1 page · 4 requests · 4 hosts",
		"1 of 4 hosts failed", `name="group" value="example.com" checked`, "first-party", "tracker",
		"net::ERR_CONNECTION_RESET · 1×", "ex in Main", "IP literals can&#39;t be routed (domains only): 203.0.113.5 · 1 request",
		`name="group" value="google-analytics.com"`, `name="host" value="static.example.com"`, "Create the service",
		`value="Example Domain"`, `value="example-domain"`, `name="list" value="1" checked`, `formaction="`+run+`/add"`,
		`data-key=" "`, "data-enter", "/routing/services/add?selector=v2fly%3Aexample")
	if strings.Contains(body, `name="group" value="google-analytics.com" checked`) {
		t.Error("the tracker is ticked")
	}

	shot := h.Login.Get(run + "/screenshots/1-1.jpg")
	if shot.Code != http.StatusOK || shot.Header().Get("Content-Type") != "image/jpeg" || shot.Body.Len() != 4 {
		t.Errorf("screenshot: %d %s", shot.Code, shot.Header().Get("Content-Type"))
	}
	if rec := h.Login.Get(run + "/screenshots/..%2F..%2Fproxier.db"); rec.Code != http.StatusNotFound {
		t.Errorf("a path out of the run: %d", rec.Code)
	}

	// nothing ticked: refused, the form kept
	rec = h.Login.Post(run+"/create", url.Values{"name": {"Example"}, "tag": {"example-site"}, "list": {"1"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Tick at least one domain.") {
		t.Errorf("no picks: %d", rec.Code)
	}
	rec = h.Login.Post(run+"/create", url.Values{"name": {"Example"}, "tag": {"example-site"}, "list": {"1"}, "group": {"example.com"}, "host": {"www.google-analytics.com"}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/routing/services/") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if it, err := h.Mod.Services.ByTag(bg(), "example-site"); err != nil || it.Origin != "discovery" {
		t.Errorf("created: %+v %v", it, err)
	}
	rec = h.Login.Post(run+"/add", url.Values{"service": {i64s(ex)}, "host": {"static.example.com"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/routing/services/"+i64s(ex)+"?saved=1" {
		t.Errorf("add: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	rec = h.Login.Post(run+"/again", url.Values{"via": {"server:1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") == run {
		t.Fatalf("again: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := h.Login.Post(run+"/again", url.Values{"via": {"server:2"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("again through an unhealthy server: %d", rec.Code)
	}
	h.Drain()
	recent := h.Login.Get("/routing/discover").Body.String()
	has(t, "recent runs", recent, "Recent runs", ">www.example.com<", "· nl-1 ·")
	if rec := h.Login.Get("/routing/discover/999"); rec.Code != http.StatusNotFound {
		t.Errorf("a missing run: %d", rec.Code)
	}
}

func i64s(n int64) string { return strconv.FormatInt(n, 10) }

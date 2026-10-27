package fetch_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

const (
	iPhone  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	android = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36"
	windows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"
	// what a browser sends when it opens a page
	htmlAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
)

func browse(h *substest.Harness, method, path, ua, accept string) *httptest.ResponseRecorder {
	h.T.Helper()
	hd := http.Header{}
	hd.Set("User-Agent", ua)
	if accept != "" {
		hd.Set("Accept", accept)
	}
	return h.Site.Do(sitetest.Req{Method: method, Path: path, Addr: "198.51.100.23:5000", Header: hd})
}

// isPage: the link's page, with none of the headers apps read.
func isPage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("%d %q: not the page", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, k := range []string{"profile-title", "profile-update-interval", "subscription-userinfo", "content-disposition"} {
		if raw(rec.Header(), k) != nil {
			t.Errorf("the page sends %s", k)
		}
	}
	if rec.Header().Get("Vary") != "Accept, User-Agent" || rec.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("headers %v", rec.Header())
	}
	return rec.Body.String()
}

// isList: the URIs as today, never the page.
func isList(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(rec.Body.String(), "vless://") {
		t.Errorf("%s: %d %q %.60q", what, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if raw(rec.Header(), "profile-title") == nil {
		t.Errorf("%s: no profile-title", what)
	}
}

func lastFetch(t *testing.T, h *substest.Harness) (format, app, outcome string) {
	t.Helper()
	row := h.App.DB.R.QueryRowContext(bg, `SELECT format, app, outcome FROM subs_fetches ORDER BY id DESC LIMIT 1`)
	if err := row.Scan(&format, &app, &outcome); err != nil {
		t.Fatal(err)
	}
	return format, app, outcome
}

func TestBrowserGetsThePage(t *testing.T) {
	h, sub, _, token := family(t)
	settings(t, h, sub, func(s *subs.Settings) { s.Title = "Семья мама" })
	body := isPage(t, browse(h, "GET", "/s/"+token, iPhone, htmlAccept))
	u := h.Mod.Links.URL(token)
	for _, want := range []string{
		`<html lang="en">`, "<h1>Семья мама</h1>", "Recommended", "Copy link", `class="qr"`,
		// the recommended app's deep link, and Hiddify's with the title encoded
		`href="shadowrocket://add/sub://`,
		`href="hiddify://import/` + u + "#" + output.Escape("Семья мама") + `"`,
		`data-lp-copy="` + u + `"`, "Questions? Write to @tikhonp.",
		"/js/linkpage.js",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	for _, not := range []string{"htmx.min.js", "proxier.js", "vless://", "style=", "<style", "onclick"} {
		if strings.Contains(body, not) {
			t.Errorf("the page holds %s", not)
		}
	}
	if format, app, outcome := lastFetch(t, h); format != "page" || app != "Browser" || outcome != output.OK {
		t.Errorf("recorded %s %s %s", format, app, outcome)
	}
}

func TestAppsGetTheListWhateverTheyAccept(t *testing.T) {
	h, _, _, token := family(t)
	isList(t, browse(h, "GET", "/s/"+token, "Happ/1.0", htmlAccept), "Happ with text/html")
	isList(t, browse(h, "GET", "/s/"+token, "v2rayNG/1.8.5", htmlAccept), "v2rayNG with text/html")
	isList(t, browse(h, "GET", "/s/"+token, iPhone, "*/*"), "a Mozilla user agent with */*")
	isList(t, browse(h, "GET", "/s/"+token, iPhone, ""), "a Mozilla user agent with no Accept")
	isList(t, browse(h, "GET", "/s/"+token+"?format=uri-plain", iPhone, htmlAccept), "a browser with ?format=uri-plain")
	var pages int
	_ = h.App.DB.R.GetContext(bg, &pages, `SELECT count(*) FROM subs_fetches WHERE format = 'page'`)
	if pages != 0 || fetches(t, h) != 5 {
		t.Errorf("%d page views of %d fetches", pages, fetches(t, h))
	}
}

// Chrome on Android has Google fetch a page it opens, asking for */*: that
// fetch got the URIs once. A link preview gets the page, recorded as such.
func TestLinkPreviewsGetThePage(t *testing.T) {
	h, _, _, token := family(t)
	readAloud := android + " (compatible; Google-Read-Aloud; +https://support.google.com/webmasters/answer/1061943)"
	for _, ua := range []string{readAloud, "TelegramBot (like TwitterBot)"} {
		for _, accept := range []string{"*/*", ""} {
			if body := isPage(t, browse(h, "GET", "/s/"+token, ua, accept)); strings.Contains(body, "vless://") {
				t.Errorf("%s with %q: the page holds a URI", ua, accept)
			}
			if format, app, _ := lastFetch(t, h); format != "page" || app != "Link preview" {
				t.Errorf("%s: recorded %s %s", ua, format, app)
			}
		}
	}
	isList(t, browse(h, "GET", "/s/"+token+"?format=uri-plain", readAloud, "*/*"), "a link preview with ?format=uri-plain")
}

func TestBrowserHeadIsThePagesHeaders(t *testing.T) {
	h, _, _, token := family(t)
	rec := browse(h, "HEAD", "/s/"+token, iPhone, htmlAccept)
	isPage(t, rec)
	if rec.Body.Len() != 0 {
		t.Errorf("a body: %q", rec.Body.String())
	}
	if fetches(t, h) != 0 {
		t.Errorf("%d fetches recorded", fetches(t, h))
	}
}

// A link that no longer works shows its message and the contact, and
// nothing a person could connect with.
func TestStubLinksShowOnlyTheirMessage(t *testing.T) {
	for _, c := range []struct {
		name, want, outcome string
		set                 func(h *substest.Harness, id int64)
	}{
		{"disabled", "⛔ Link disabled", output.StubDisabled, func(h *substest.Harness, id int64) {
			if err := h.Mod.Links.Disable(bg, id, access.All, "admin"); err != nil {
				t.Fatal(err)
			}
		}},
		{"expired in Russian", "⏳ Истекла 1 дек 2026", output.StubExpired, func(h *substest.Harness, id int64) {
			if err := h.Mod.Links.Edit(bg, id, access.All, editOf(h, id, func(e *editForm) { e.Lang = "ru" }), "admin"); err != nil {
				t.Fatal(err)
			}
			expire(t, h, id, "2026-12-01", "")
			h.Now = time.Date(2026, 12, 3, 9, 0, 0, 0, time.UTC)
		}},
		{"deleted", "⛔ Link removed", output.StubDeleted, func(h *substest.Harness, id int64) {
			if err := h.Mod.Links.Delete(bg, id, access.All, "admin"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _, id, token := family(t)
			c.set(h, id)
			body := isPage(t, browse(h, "GET", "/s/"+token, iPhone, htmlAccept))
			if !strings.Contains(body, "<h1>"+c.want+"</h1>") {
				t.Errorf("no message %q", c.want)
			}
			if !strings.Contains(body, "@tikhonp") {
				t.Error("no contact")
			}
			for _, not := range []string{token, "://add", "://import", "install-", "data-lp-copy", `class="qr"`, "lp-tab"} {
				if strings.Contains(body, not) {
					t.Errorf("the page holds %s", not)
				}
			}
			if format, _, outcome := lastFetch(t, h); format != "page" || outcome != c.outcome {
				t.Errorf("recorded %s %s", format, outcome)
			}
		})
	}
}

func TestRussianStubPageIsInRussian(t *testing.T) {
	h, _, id, token := family(t)
	if err := h.Mod.Links.Edit(bg, id, access.All, editOf(h, id, func(e *editForm) { e.Lang = "ru" }), "admin"); err != nil {
		t.Fatal(err)
	}
	body := isPage(t, browse(h, "GET", "/s/"+token, android, htmlAccept))
	for _, want := range []string{`<html lang="ru">`, "Добавить в Happ", "Есть вопросы? Пишите @tikhonp."} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
}

func TestEmptySubscriptionPageSaysServersWillCome(t *testing.T) {
	h := substest.New(t)
	_, token := h.Link(h.Subscription("Empty"), "Mom")
	body := isPage(t, browse(h, "GET", "/s/"+token, iPhone, htmlAccept))
	for _, want := range []string{"No servers here yet", `href="shadowrocket://add/sub://`, `data-lp-copy="`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	if format, _, outcome := lastFetch(t, h); format != "page" || outcome != output.StubEmpty {
		t.Errorf("recorded %s %s", format, outcome)
	}
}

func TestUnknownTokenInABrowserIsThePlain404(t *testing.T) {
	h, _, _, _ := family(t)
	want := h.Fetch("GET", "/s/"+strings.Repeat("A", 43), "", "")
	rec := browse(h, "GET", "/s/"+strings.Repeat("A", 43), iPhone, htmlAccept)
	if rec.Code != 404 || rec.Body.String() != want.Body.String() || rec.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
		t.Errorf("%d %q, want %q", rec.Code, rec.Body.String(), want.Body.String())
	}
}

func TestOpenTabFollowsTheUserAgent(t *testing.T) {
	h, _, _, token := family(t)
	for ua, want := range map[string]string{iPhone: "ios", android: "android", windows: "windows", "Mozilla/5.0": "ios"} {
		body := isPage(t, browse(h, "GET", "/s/"+token, ua, htmlAccept))
		if !strings.Contains(body, `data-open="`+want+`"`) || !strings.Contains(body, `class="lp-tab lp-on" data-lp-tab="`+want+`"`) {
			t.Errorf("%s: not open on %s", ua, want)
		}
	}
}

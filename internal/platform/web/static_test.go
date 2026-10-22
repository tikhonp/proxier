package web_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

func TestStaticAssets(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	base := "/static/" + ui.StaticHash + "/"

	rec := s.Do(sitetest.Req{Path: base + "css/app.css"})
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("css: %d %v", rec.Code, rec.Header())
	}
	if rec := s.Do(sitetest.Req{Path: base + "fonts/plex-mono-latin-400-normal.woff2"}); rec.Code != 200 || rec.Header().Get("Content-Type") != "font/woff2" {
		t.Errorf("font: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := s.Do(sitetest.Req{Path: base + "js/proxier.js"}); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("js: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/static/000000000000/css/app.css", base + "nope.css", base + "../ui.go", base + "css/../../x"} {
		if rec := s.Do(sitetest.Req{Path: p}); rec.Code != 404 {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

// A signed-out browser or iPhone gets the icons at the paths it asks for
// unprompted, and every page links them for Safari and the home screen.
func TestIcons(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	for p, ct := range map[string]string{
		"/favicon.ico":                      "image/x-icon",
		"/apple-touch-icon.png":             "image/png",
		"/apple-touch-icon-precomposed.png": "image/png",
	} {
		rec := s.Do(sitetest.Req{Path: p})
		if rec.Code != 200 || rec.Header().Get("Content-Type") != ct || rec.Header().Get("Cache-Control") != "public, max-age=86400" || rec.Body.Len() == 0 {
			t.Errorf("%s: %d %v", p, rec.Code, rec.Header())
		}
	}

	page := s.Do(sitetest.Req{Path: "/login"}).Body.String()
	for _, link := range []string{
		`<link rel="icon" href="/static/` + ui.StaticHash + `/icons/favicon.ico" sizes="48x48">`,
		`<link rel="icon" href="/static/` + ui.StaticHash + `/icons/icon.svg" type="image/svg+xml">`,
		`<link rel="apple-touch-icon" href="/static/` + ui.StaticHash + `/icons/apple-touch-icon.png">`,
		`<meta name="apple-mobile-web-app-title" content="Proxier">`,
	} {
		if !strings.Contains(page, link) {
			t.Errorf("sign-in page lacks %s", link)
		}
	}
	for _, name := range []string{"icons/favicon.ico", "icons/icon.svg", "icons/apple-touch-icon.png"} {
		if rec := s.Do(sitetest.Req{Path: ui.Asset(name)}); rec.Code != 200 {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
}

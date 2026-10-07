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

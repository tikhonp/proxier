package web_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/httpx"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func TestCrossSitePostIsRefused(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/me/language", Cookies: []*http.Cookie{l.Cookie},
		Form: url.Values{"lang": {"ru"}, "_csrf": {l.CSRF}}, Header: http.Header{"Sec-Fetch-Site": {"cross-site"}}})
	if rec.Code != 403 {
		t.Fatalf("status %d, want 403", rec.Code)
	}
	if a, _ := s.App.Auth.Admin(t.Context()); a.Language != "en" {
		t.Error("the language changed")
	}
	// the same request from the same site works
	rec = s.Do(sitetest.Req{Method: "POST", Path: "/me/language", Cookies: []*http.Cookie{l.Cookie},
		Form: url.Values{"lang": {"ru"}, "_csrf": {l.CSRF}}, Header: http.Header{"Sec-Fetch-Site": {"same-origin"}}})
	if rec.Code != 303 {
		t.Fatalf("same-origin status %d", rec.Code)
	}
}

func TestCSRFTokenIsTiedToTheSession(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	a, b := s.SignIn(""), s.SignIn("198.51.100.4:1")
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/me/language", Cookies: []*http.Cookie{a.Cookie},
		Form: url.Values{"lang": {"ru"}, "_csrf": {b.CSRF}}})
	if rec.Code != 403 {
		t.Fatalf("a token from another session: status %d", rec.Code)
	}
}

func TestCSRFTokenInHTMXHeader(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/me/language", Cookies: []*http.Cookie{l.Cookie},
		Form: url.Values{"lang": {"ru"}}, Header: http.Header{web.CSRFHeader: {l.CSRF}, "HX-Request": {"true"}}})
	if rec.Code != 200 || rec.Header().Get("HX-Redirect") == "" {
		t.Fatalf("status %d, HX-Redirect %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	for _, https := range []bool{true, false} {
		s := sitetest.New(t, sitetest.Options{HTTPS: https})
		rec := s.Do(sitetest.Req{Method: "POST", Path: "/login", Form: url.Values{"username": {sitetest.Username}, "password": {sitetest.Password}}})
		if rec.Code != 303 {
			t.Fatalf("https=%v: status %d", https, rec.Code)
		}
		cs := rec.Result().Cookies()
		if len(cs) != 1 {
			t.Fatalf("https=%v: %d cookies", https, len(cs))
		}
		c := cs[0]
		wantName := "proxier_session"
		if https {
			wantName = "__Host-proxier_session"
		}
		if c.Name != wantName || !c.HttpOnly || c.Secure != https || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
			t.Errorf("https=%v: cookie %+v", https, c)
		}
		if c.Expires.IsZero() || c.MaxAge != 0 {
			t.Errorf("https=%v: want Expires set and no Max-Age: %+v", https, c)
		}
	}
}

func TestPublicRoutesIgnoreCookies(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	// no public route exists in Phase 0; an unknown path under /s/ must answer
	// as a public one: plain 404, no redirect to /login, nothing set.
	for _, p := range []string{"/s/abc", "/r/abc", "/f/abc"} {
		rec := s.Do(sitetest.Req{Path: p, Cookies: []*http.Cookie{l.Cookie}})
		if rec.Code != 404 || rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("Location") != "" {
			t.Errorf("%s: status %d, headers %v", p, rec.Code, rec.Header())
		}
		if got := rec.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
			t.Errorf("%s: X-Robots-Tag %q", p, got)
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("%s: public 404 must be plain text", p)
		}
	}
}

func TestHTMXRequestWithoutSessionGetsHXRedirect(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	rec := s.Do(sitetest.Req{Path: "/settings/security", Header: http.Header{"HX-Request": {"true"}, "HX-Current-URL": {"http://proxier.test/servers/de-1?tab=health"}}})
	if rec.Code != 401 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/login?next=%2Fservers%2Fde-1%3Ftab%3Dhealth" {
		t.Errorf("HX-Redirect %q", got)
	}
}

func TestExpiredSessionRedirectsWithReason(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	s.Advance(7*24*time.Hour + time.Minute)
	rec := s.Do(sitetest.Req{Path: "/settings/security", Cookies: []*http.Cookie{l.Cookie}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/login?ended=idle&next=%2Fsettings%2Fsecurity" {
		t.Fatalf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	page := s.Do(sitetest.Req{Path: rec.Header().Get("Location")})
	if !strings.Contains(page.Body.String(), "Signed out after 7 days idle. You&#39;ll go back to /settings/security.") {
		t.Errorf("sign-in page lacks the message:\n%s", page.Body.String())
	}
}

func TestUnknownPathRedirectsToLoginWhenSignedOut(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	for _, p := range []string{"/servers", "/nope/at/all", "/settings/zzz"} {
		rec := s.Do(sitetest.Req{Path: p})
		if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
			t.Errorf("%s: status %d, Location %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	// signed in, the same path is a real 404 page
	l := s.SignIn("")
	if rec := l.Get("/nope/at/all"); rec.Code != 404 || !strings.Contains(rec.Body.String(), "Not found") {
		t.Errorf("signed in: status %d", rec.Code)
	}
}

func TestAdminPagesSendCSP(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{HTTPS: true})
	l := s.SignIn("")
	for _, rec := range []interface{ Header() http.Header }{l.Get("/"), s.Do(sitetest.Req{Path: "/login"})} {
		h := rec.Header()
		for k, want := range map[string]string{
			"Content-Security-Policy":    "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
			"X-Content-Type-Options":     "nosniff",
			"Referrer-Policy":            "same-origin",
			"X-Frame-Options":            "DENY",
			"Cross-Origin-Opener-Policy": "same-origin",
			"Permissions-Policy":         "camera=(), microphone=(), geolocation=()",
			"Strict-Transport-Security":  "max-age=31536000",
			"Cache-Control":              "no-store",
		} {
			if got := h.Get(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
	}
	// no HSTS over plain http
	p := sitetest.New(t, sitetest.Options{})
	if got := p.Do(sitetest.Req{Path: "/login"}).Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS over http: %q", got)
	}
}

func TestTokenPathsAreMaskedInLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	e := httpx.New(httpx.Options{Log: log})
	r := web.Mount(e, web.Deps{Log: log, BaseURL: &url.URL{Scheme: "http", Host: "proxier.test"}}, ui.StaticFS, ui.StaticHash)
	boom := func(*echo.Context) error { return errors.New("boom") }
	r.Public.GET("/s/:token", boom)
	r.Public.GET("/r/:token/x.conf", boom)
	const token = "Zm9vYmFyYmF6cXV4cXV1eGNvcmdlZ3JhdWx0Z2FycGx5"
	for _, p := range []string{"/s/" + token, "/r/" + token + "/x.conf"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != 500 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	out := buf.String()
	if strings.Contains(out, token) {
		t.Errorf("a token reached the log:\n%s", out)
	}
	for _, want := range []string{"msg=handler", "path=/s/•••", "path=/r/•••/x.conf", "msg=request"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %q:\n%s", want, out)
		}
	}
	for p, want := range map[string]string{
		"/s/abc": "/s/•••", "/r/abc/home.conf": "/r/•••/home.conf", "/f/abc/": "/f/•••/", "/s/": "/s/", "/s": "/s",
		"/servers/1": "/servers/1", "/agent/v1/files": "/agent/v1/files",
	} {
		if got := httpx.MaskPath(p); got != want {
			t.Errorf("MaskPath(%q) = %q, want %q", p, got, want)
		}
	}
}

// Package sitetest runs the whole HTTP app in tests: the platform with its
// migrations, an admin, and requests through the real route tree. Only tests
// import it.
package sitetest

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const (
	Username = "admin"
	Password = "correct horse battery"
	// DefaultAddr is the socket address of requests that name none.
	DefaultAddr = "192.0.2.1:4000"
)

// Options tune the site.
type Options struct {
	HTTPS          bool
	TrustedProxies []netip.Prefix
	Modules        []module.Module
	NoAdmin        bool
	// Log receives the app's log as text (the request log included); nil
	// discards it.
	Log io.Writer
}

// Site is a running app.
type Site struct {
	T       *testing.T
	App     *platform.App
	Handler http.Handler
	Now     time.Time // the clock of auth; move it with Advance
}

// New opens, migrates and (unless NoAdmin) creates the admin.
func New(t *testing.T, o Options) *Site {
	t.Helper()
	tz, _ := time.LoadLocation("Europe/Moscow")
	scheme := "http"
	if o.HTTPS {
		scheme = "https"
	}
	cfg := &config.Config{
		DataDir:        t.TempDir() + "/data",
		MasterKey:      bytes.Repeat([]byte{7}, config.MasterKeySize),
		BaseURL:        &url.URL{Scheme: scheme, Host: "proxier.test"},
		TrustedProxies: o.TrustedProxies,
		TZ:             tz,
	}
	logTo := o.Log
	if logTo == nil {
		logTo = io.Discard
	}
	app, err := platform.Open(cfg, slog.New(slog.NewTextHandler(logTo, nil)), o.Modules...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := &Site{T: t, App: app, Now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	app.Auth.Params = auth.Params{Memory: 1024, Time: 1, Threads: 1}
	app.Auth.Now = func() time.Time { return s.Now }
	app.Auth.Sleep = func(context.Context, time.Duration) {}
	s.Handler = app.HTTP()
	if !o.NoAdmin {
		if err := app.Auth.CreateAdmin(context.Background(), Username, Password); err != nil {
			t.Fatalf("create admin: %v", err)
		}
	}
	return s
}

// Advance moves the clock.
func (s *Site) Advance(d time.Duration) { s.Now = s.Now.Add(d) }

// Req is one request.
type Req struct {
	Method, Path string
	Form         url.Values
	Header       http.Header
	Cookies      []*http.Cookie
	Addr         string // socket address; DefaultAddr when empty
	Body         []byte // a raw body (multipart); used instead of Form
	ContentType  string
}

// Do serves r and returns the recorded response.
func (s *Site) Do(r Req) *httptest.ResponseRecorder {
	s.T.Helper()
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if r.Form != nil {
		body = strings.NewReader(r.Form.Encode())
	}
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req := httptest.NewRequest(method, r.Path, body)
	if r.Form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", r.ContentType)
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for _, c := range r.Cookies {
		req.AddCookie(c)
	}
	req.RemoteAddr = DefaultAddr
	if r.Addr != "" {
		req.RemoteAddr = r.Addr
	}
	rec := httptest.NewRecorder()
	s.Handler.ServeHTTP(rec, req)
	return rec
}

// Login is a signed-in browser.
type Login struct {
	Site   *Site
	Cookie *http.Cookie
	Token  string
	CSRF   string
	ID     int64
}

// SignIn signs the admin in from addr ("" for the default) and returns the browser.
func (s *Site) SignIn(addr string) *Login {
	s.T.Helper()
	res, err := s.App.Auth.SignIn(context.Background(), Username, Password, hostOf(addr), "Mozilla/5.0 (Macintosh) Chrome/120 Safari/537")
	if err != nil {
		s.T.Fatalf("sign in: %v", err)
	}
	return &Login{
		Site:   s,
		Cookie: &http.Cookie{Name: web.CookieName(s.App.Cfg.BaseURL.Scheme == "https"), Value: res.Session.Token},
		Token:  res.Session.Token, CSRF: res.Session.CSRF, ID: res.Session.ID,
	}
}

func hostOf(addr string) string {
	if addr == "" {
		addr = DefaultAddr
	}
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap.Addr().String()
	}
	return addr
}

// Get is a signed-in GET.
func (l *Login) Get(path string, h ...http.Header) *httptest.ResponseRecorder {
	r := Req{Path: path, Cookies: []*http.Cookie{l.Cookie}}
	if len(h) > 0 {
		r.Header = h[0]
	}
	return l.Site.Do(r)
}

// Post is a signed-in POST with the CSRF field filled in.
func (l *Login) Post(path string, form url.Values) *httptest.ResponseRecorder {
	if form == nil {
		form = url.Values{}
	}
	if form.Get("_csrf") == "" {
		form.Set("_csrf", l.CSRF)
	}
	return l.Site.Do(Req{Method: http.MethodPost, Path: path, Form: form, Cookies: []*http.Cookie{l.Cookie}})
}

// File is an uploaded file of PostMultipart.
type File struct {
	Name string
	Data []byte
}

// PostMultipart is a signed-in multipart POST (an upload) with the CSRF field
// filled in.
func (l *Login) PostMultipart(path string, fields url.Values, files map[string]File) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if fields.Get("_csrf") == "" {
		if fields == nil {
			fields = url.Values{}
		}
		fields.Set("_csrf", l.CSRF)
	}
	for k, vs := range fields {
		for _, v := range vs {
			_ = w.WriteField(k, v)
		}
	}
	for k, f := range files {
		fw, _ := w.CreateFormFile(k, f.Name)
		_, _ = fw.Write(f.Data)
	}
	_ = w.Close()
	return l.Site.Do(Req{Method: http.MethodPost, Path: path, Body: buf.Bytes(), ContentType: w.FormDataContentType(), Cookies: []*http.Cookie{l.Cookie}})
}

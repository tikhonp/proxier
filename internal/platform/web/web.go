// Package web is the HTTP plumbing of the admin UI: the route groups, the
// session, CSRF and header middleware, rendering. It depends on auth and
// i18n; the views live in ui and the handlers in pages.
package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

const (
	CSRFHeader = "X-CSRF-Token"
	CSRFField  = "_csrf"
)

// publicPrefixes are the URL spaces that never touch cookies (ADR 0006).
var publicPrefixes = []string{"/s/", "/r/", "/f/"}

func isPublicPath(p string) bool {
	for _, pre := range publicPrefixes {
		if strings.HasPrefix(p, pre) || p == strings.TrimSuffix(pre, "/") {
			return true
		}
	}
	return false
}

// Routes are the groups modules add routes to.
type Routes struct {
	Admin  *echo.Group // session required, CSRF on unsafe methods, admin headers
	Public *echo.Group // never reads or sets cookies; no-store, noindex (rate limits: Phase 2)
	Open   *echo.Group // no session needed, admin headers: /login
	// Shell builds the layout's data (title already translated, current
	// path) for a page of a module; the platform fills it in before the
	// modules' routes are declared.
	Shell func(c *echo.Context, title, path string) ui.Shell
}

// Request is what the middleware knows about the current request.
type Request struct {
	Admin   *auth.Admin   // nil when signed out
	Session *auth.Session // nil when signed out
	Loc     *i18n.Localizer
	HTMX    bool
}

type ctxKey struct{}

// FromContext returns the Request of ctx; an empty one outside the middleware.
func FromContext(ctx context.Context) *Request {
	if r, ok := ctx.Value(ctxKey{}).(*Request); ok {
		return r
	}
	return &Request{Loc: i18n.From(ctx)}
}

// Deps is what the middleware needs.
type Deps struct {
	Log      *slog.Logger
	Auth     *auth.Service
	I18n     *i18n.Catalog
	Settings *settings.Store
	BaseURL  *url.URL
}

// current returns the request's Request, creating it on first use.
func current(c *echo.Context) *Request {
	r := c.Request()
	if q, ok := r.Context().Value(ctxKey{}).(*Request); ok {
		return q
	}
	q := &Request{HTMX: r.Header.Get("HX-Request") == "true", Loc: i18n.From(r.Context())}
	c.SetRequest(r.WithContext(context.WithValue(r.Context(), ctxKey{}, q)))
	return q
}

// Render writes comp with status. The page is built first, so an error in a
// template never leaves half a page.
func Render(c *echo.Context, status int, comp templ.Component) error {
	var buf bytes.Buffer
	ctx := c.Request().Context()
	if q := current(c); q.Session != nil {
		ctx = ui.WithCSRF(ctx, q.Session.CSRF)
	}
	if err := comp.Render(ctx, &buf); err != nil {
		return err
	}
	return c.Blob(status, "text/html; charset=utf-8", buf.Bytes())
}

// Redirect answers 303, or 200 + HX-Redirect for an htmx request.
func Redirect(c *echo.Context, to string) error {
	if c.Request().Header.Get("HX-Request") == "true" {
		c.Response().Header().Set("HX-Redirect", to)
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, to)
}

// SafeNext returns raw when it is a local path that may follow sign-in, else "/".
func SafeNext(raw string) string {
	if raw == "" || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return "/"
	}
	if strings.ContainsAny(raw, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	if u.Path == "/login" || u.Path == "/logout" {
		return "/"
	}
	return raw
}

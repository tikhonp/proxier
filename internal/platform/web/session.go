package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// CookieName is __Host-proxier_session over https, proxier_session over http.
func CookieName(secure bool) string {
	if secure {
		return "__Host-proxier_session"
	}
	return "proxier_session"
}

// SetSessionCookie sends the cookie of a new session.
func SetSessionCookie(c *echo.Context, secure bool, s auth.Session) {
	c.SetCookie(&http.Cookie{
		Name: CookieName(secure), Value: s.Token, Path: "/", Expires: s.ExpiresAt.Time,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie removes the cookie.
func ClearSessionCookie(c *echo.Context, secure bool) {
	c.SetCookie(&http.Cookie{
		Name: CookieName(secure), Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// resolve reads the cookie and fills the Request. It returns the reason a
// cookie was refused: "" no cookie or an unknown one, "idle"/"absolute" for an
// expired session.
func resolve(a *auth.Service, secure bool, c *echo.Context) (expired string, err error) {
	ck, cerr := c.Cookie(CookieName(secure))
	if cerr != nil || ck.Value == "" {
		return "", nil
	}
	sess, aerr := a.Authenticate(c.Request().Context(), ck.Value, c.RealIP())
	var xe *auth.ExpiredError
	switch {
	case aerr == nil:
		admin, err := a.Admin(c.Request().Context())
		if err != nil {
			return "", err
		}
		q := current(c)
		q.Session, q.Admin = &sess, &admin
		return "", nil
	case errors.As(aerr, &xe):
		return xe.Reason, nil
	case errors.Is(aerr, auth.ErrNoSession):
		return "", nil
	}
	return "", aerr
}

// OptionalSession fills the Request when the cookie is valid and lets the
// request through either way (/login).
func OptionalSession(a *auth.Service, secure bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if _, err := resolve(a, secure, c); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// Session requires a valid session. Without one a page request is redirected
// to /login?next=…, and an htmx request gets 401 + HX-Redirect, since htmx
// would swap the login page into a fragment.
func Session(a *auth.Service, secure bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			expired, err := resolve(a, secure, c)
			if err != nil {
				return err
			}
			if current(c).Session != nil {
				return next(c)
			}
			r := c.Request()
			q := url.Values{}
			if expired != "" {
				q.Set("ended", expired)
				ClearSessionCookie(c, secure)
			}
			htmx := r.Header.Get("HX-Request") == "true"
			switch {
			case htmx:
				if cur, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && cur.Path != "" {
					q.Set("next", cur.RequestURI())
				}
			case r.Method == http.MethodGet || r.Method == http.MethodHead:
				q.Set("next", r.URL.RequestURI())
			}
			to := "/login"
			if len(q) > 0 {
				to += "?" + q.Encode()
			}
			if htmx {
				c.Response().Header().Set("HX-Redirect", to)
				return c.NoContent(http.StatusUnauthorized)
			}
			return c.Redirect(http.StatusSeeOther, to)
		}
	}
}

// Localize picks the language and display time zone and puts the Localizer in
// the request context. Signed in: the admin's language. Signed out: the
// browser's, then the admin's, then English.
func Localize(a *auth.Service, cat *i18n.Catalog, st *settings.Store) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ctx := c.Request().Context()
			q := current(c)
			lang := i18n.EN
			switch {
			case q.Admin != nil:
				lang = q.Admin.Language
			default:
				if l, ok := i18n.Match(c.Request().Header.Get("Accept-Language")); ok {
					lang = l
				} else if ad, err := a.Admin(ctx); err == nil {
					lang = ad.Language
				}
			}
			tz := time.UTC
			if name, err := st.Get(ctx, "general.time_zone"); err == nil {
				if loc, err := time.LoadLocation(name); err == nil {
					tz = loc
				}
			}
			q.Loc = cat.Localizer(lang, tz)
			c.Response().Header().Set("Content-Language", string(lang))
			c.SetRequest(c.Request().WithContext(i18n.WithLocalizer(c.Request().Context(), q.Loc)))
			return next(c)
		}
	}
}

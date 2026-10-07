package web

import (
	"net/http"
	"net/url"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
)

// CrossOrigin refuses cross-site unsafe requests (Sec-Fetch-Site, Origin)
// before anything else reads them. The base URL's origin is trusted because
// the gateway may rewrite Host.
func CrossOrigin(baseURL *url.URL) echo.MiddlewareFunc {
	cop := http.NewCrossOriginProtection()
	if baseURL != nil {
		_ = cop.AddTrustedOrigin(baseURL.Scheme + "://" + baseURL.Host)
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if err := cop.Check(c.Request()); err != nil {
				return echo.NewHTTPError(http.StatusForbidden, "cross-origin request refused")
			}
			return next(c)
		}
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// CSRF requires the session's token on every unsafe request: in the
// X-CSRF-Token header (htmx) or the _csrf form field.
func CSRF(a *auth.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if safeMethod(c.Request().Method) {
				return next(c)
			}
			s := current(c).Session
			if s == nil {
				return echo.NewHTTPError(http.StatusForbidden, "no session")
			}
			got := c.Request().Header.Get(CSRFHeader)
			if got == "" {
				got = c.FormValue(CSRFField)
			}
			if !a.CheckCSRF(s.Token, got) {
				return echo.NewHTTPError(http.StatusForbidden, "bad CSRF token")
			}
			return next(c)
		}
	}
}

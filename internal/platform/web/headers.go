package web

import (
	"github.com/labstack/echo/v5"
)

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

func baseHeaders(c *echo.Context, httpsBase bool) {
	h := c.Response().Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if httpsBase {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

// SecureHeaders sets the CSP and the other security headers; admin pages are
// never cached.
func SecureHeaders(httpsBase bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			baseHeaders(c, httpsBase)
			c.Response().Header().Set("Cache-Control", "no-store")
			return next(c)
		}
	}
}

// PublicHeaders is for /s/, /r/ and /f/: no-store, noindex, and the request's
// cookies are removed so nothing downstream can read them.
func PublicHeaders() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Request().Header.Del("Cookie")
			baseHeaders(c, false)
			h := c.Response().Header()
			h.Set("Cache-Control", "no-store")
			h.Set("X-Robots-Tag", "noindex, nofollow")
			return next(c)
		}
	}
}

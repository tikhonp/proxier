package web

import (
	"io/fs"

	"github.com/labstack/echo/v5"
)

// Mount creates the route groups on e, serves the static files and installs
// the error handler. Modules add their routes to the returned groups.
//
// Echo's automatic per-group 404 routes are off (see httpx.New): one explicit
// catch-all sends an unknown path through the admin chain, so a signed-out
// visitor learns nothing about which paths exist, except under /s/, /r/, /f/,
// which never touch cookies.
func Mount(e *echo.Echo, d Deps, static fs.FS, staticHash string) Routes {
	https := d.BaseURL != nil && d.BaseURL.Scheme == "https"
	e.HTTPErrorHandler = ErrorHandler(d.Log)

	e.GET("/static/:hash/*", Static(static, staticHash), SecureHeaders(https))

	adminChain := []echo.MiddlewareFunc{
		SecureHeaders(https), CrossOrigin(d.BaseURL), Session(d.Auth, https), CSRF(d.Auth), Localize(d.Auth, d.I18n, d.Settings),
	}
	openChain := []echo.MiddlewareFunc{
		SecureHeaders(https), CrossOrigin(d.BaseURL), OptionalSession(d.Auth, https), Localize(d.Auth, d.I18n, d.Settings),
	}
	publicChain := []echo.MiddlewareFunc{PublicHeaders()}

	notFound := func(*echo.Context) error { return echo.ErrNotFound }
	e.RouteNotFound("/*", func(c *echo.Context) error {
		chain := adminChain
		if isPublicPath(c.Request().URL.Path) {
			chain = publicChain
		}
		h := echo.HandlerFunc(notFound)
		for i := len(chain) - 1; i >= 0; i-- {
			h = chain[i](h)
		}
		return h(c)
	})

	return Routes{
		Admin:  e.Group("", adminChain...),
		Public: e.Group("", publicChain...),
		Open:   e.Group("", openChain...),
	}
}

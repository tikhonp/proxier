package web

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// ErrorHandler answers errors with a page (403, 404, 500) in the admin
// space and with plain text on public routes, where nothing may be revealed.
func ErrorHandler(log *slog.Logger) echo.HTTPErrorHandler {
	return func(c *echo.Context, err error) {
		if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
			return
		}
		code := echo.StatusCode(err)
		if code == 0 {
			code = http.StatusInternalServerError
		}
		if code >= 500 {
			log.Error("handler", "error", err, "path", c.Request().URL.Path)
		}
		if c.Request().Method == http.MethodHead {
			_ = c.NoContent(code)
			return
		}
		if isPublicPath(c.Request().URL.Path) {
			_ = c.String(code, http.StatusText(code)+"\n")
			return
		}
		if rerr := Render(c, code, ui.ErrorPage(code)); rerr != nil {
			_ = c.String(code, http.StatusText(code)+"\n")
		}
	}
}

package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/httpx"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// ErrorHandler answers errors with a page (403, 404, 500) in the signed-in
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
			log.Error("handler", "error", err, "path", httpx.MaskPath(c.Request().URL.Path))
		}
		if c.Request().Method == http.MethodHead {
			_ = c.NoContent(code)
			return
		}
		if isPublicPath(c.Request().URL.Path) {
			_ = c.String(code, http.StatusText(code)+"\n")
			return
		}
		page := ui.ErrorPage(code)
		var fe *ForbiddenError
		if errors.As(err, &fe) {
			page = ui.ForbiddenPage(fe.Area)
			if c.Request().Header.Get("HX-Request") == "true" {
				page = ui.ForbiddenNotice(fe.Area)
			}
		}
		if rerr := Render(c, code, page); rerr != nil {
			_ = c.String(code, http.StatusText(code)+"\n")
		}
	}
}

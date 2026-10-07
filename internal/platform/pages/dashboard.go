package pages

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) dashboard(c *echo.Context) error {
	ctx := c.Request().Context()
	return web.Render(c, http.StatusOK, dashboardPage(h.shell(c, i18n.T(ctx, "nav.dashboard"), "/")))
}

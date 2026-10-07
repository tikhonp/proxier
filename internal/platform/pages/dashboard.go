package pages

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// dashboardView is what needs attention on the dashboard.
type dashboardView struct {
	NotConfigured bool
	Failed        []notify.Notification
}

func (h *handler) dashboard(c *echo.Context) error {
	ctx := c.Request().Context()
	var v dashboardView
	// A failing health query must not take the dashboard down with it.
	if ok, err := h.Notify.Configured(ctx); err != nil {
		h.Log.Error("pages: notifications configured", "error", err)
	} else {
		v.NotConfigured = !ok
	}
	failed, err := h.Notify.Failed(ctx, time.Now().Add(-failedWindow), 20)
	if err != nil {
		h.Log.Error("pages: failed notifications", "error", err)
	}
	v.Failed = failed
	return web.Render(c, http.StatusOK, dashboardPage(h.shell(c, i18n.T(ctx, "nav.dashboard"), "/"), v))
}

package pages

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type tailnetView struct {
	Status tailnet.Status
	Saved  bool
	Err    string // translated
}

func (h *handler) tailnetPage(c *echo.Context) error {
	return h.renderTailnet(c, http.StatusOK, tailnetView{Saved: c.QueryParam("saved") == "reauth"})
}

func (h *handler) renderTailnet(c *echo.Context, status int, v tailnetView) error {
	ctx := c.Request().Context()
	v.Status = h.Tailnet.Status(ctx)
	s := h.shell(c, "Tailnet", "/settings")
	return web.Render(c, status, tailnetPage(s, h.sortedPages(), v))
}

// tailnetReauth restarts the node with a new pre-auth key. The key is used for
// that login only and never stored.
func (h *handler) tailnetReauth(c *echo.Context) error {
	ctx := c.Request().Context()
	key := strings.TrimSpace(c.FormValue("authkey"))
	if key == "" {
		return h.renderTailnet(c, http.StatusUnprocessableEntity, tailnetView{Err: i18n.T(ctx, "tailnet.authkey.empty")})
	}
	if err := h.Tailnet.Reauthenticate(ctx, key); err != nil {
		h.Log.Warn("pages: tailnet reauthenticate", "error", err)
		return h.renderTailnet(c, http.StatusUnprocessableEntity, tailnetView{Err: err.Error()})
	}
	return web.Redirect(c, "/settings/integrations/tailnet?saved=reauth")
}

// tailnetStatusKind maps the node's state to a status marker.
func tailnetStatusKind(s tailnet.State) string {
	switch s {
	case tailnet.Running:
		return "ok"
	case tailnet.Starting:
		return "running"
	case tailnet.NeedsLogin:
		return "look"
	case tailnet.Error:
		return "broken"
	}
	return "unknown"
}

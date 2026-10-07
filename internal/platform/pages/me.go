package pages

import (
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) setLanguage(c *echo.Context) error {
	l := i18n.Lang(c.FormValue("lang"))
	if !l.Valid() {
		return echo.NewHTTPError(400, "unknown language")
	}
	if err := h.Auth.SetLanguage(c.Request().Context(), l); err != nil {
		return err
	}
	return web.Redirect(c, web.SafeNext(c.FormValue("next")))
}

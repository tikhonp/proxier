package pages

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type settingsView struct {
	Values []settings.Value
	Errs   settings.FieldErrors
	Saved  bool
}

func (h *handler) settingsPage(c *echo.Context) error {
	return h.renderSettings(c, http.StatusOK, nil, nil, c.QueryParam("saved") == "1")
}

func (h *handler) renderSettings(c *echo.Context, status int, submitted map[string]string, errs settings.FieldErrors, saved bool) error {
	ctx := c.Request().Context()
	vals := make([]settings.Value, 0, len(conf.Section.Fields))
	for _, f := range conf.Section.Fields {
		v, err := h.Settings.Lookup(ctx, f.Key)
		if err != nil {
			return err
		}
		if raw, ok := submitted[f.Key]; ok {
			v.Value = raw
		}
		vals = append(vals, v)
	}
	var pages []ui.SettingsPage
	if h.settingsPages != nil {
		pages = h.settingsPages()
	}
	s := h.shell(c, i18n.T(ctx, "settings.subscriptions"), "/settings")
	return web.Render(c, status, subsSettingsPage(s, pages, conf.Section, settingsView{Values: vals, Errs: errs, Saved: saved}))
}

func (h *handler) saveSettings(c *echo.Context) error {
	ctx := c.Request().Context()
	form, _ := c.FormValues()
	values := map[string]string{}
	for _, f := range conf.Section.Fields {
		if vs, ok := form[f.Key]; ok && len(vs) > 0 {
			values[f.Key] = vs[0]
		}
	}
	if err := h.Settings.Set(ctx, events.ActorAdmin, conf.Section.Name, values); err != nil {
		if fe, ok := err.(settings.FieldErrors); ok {
			return h.renderSettings(c, http.StatusUnprocessableEntity, values, fe, false)
		}
		return err
	}
	return web.Redirect(c, "/settings/subscriptions?saved=1")
}

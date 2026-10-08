package pages

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type settingsView struct {
	Groups []ui.SettingsGroup
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
		if raw, ok := submitted[f.Key]; ok && f.Kind != settings.Secret {
			v.Value = raw
		}
		vals = append(vals, v)
	}
	groups := []ui.SettingsGroup{
		{Title: i18n.T(ctx, "routing.settings.refresh"), Keys: []string{conf.RefreshAt, conf.ShrinkMin, conf.ShrinkPct}},
		{Title: i18n.T(ctx, "routing.settings.catalog"), Keys: []string{conf.CatalogAt, conf.GitHubToken}},
		{Title: i18n.T(ctx, "routing.settings.routers"), Note: i18n.T(ctx, "routing.settings.routers.note"),
			Keys: []string{conf.SyncDelay, conf.DriftEvery, conf.DriftRepair}},
	}
	var pages []ui.SettingsPage
	if h.settingsPages != nil {
		pages = h.settingsPages()
	}
	s := h.shell(c, i18n.T(ctx, "settings.routing"), "/settings")
	return web.Render(c, status, routingSettingsPage(s, pages, settingsView{Groups: groups, Values: vals, Errs: errs, Saved: saved}))
}

// saveSettings saves the form. The GitHub token's field is empty unless the
// admin types a new one: empty keeps it, its clear box removes it.
func (h *handler) saveSettings(c *echo.Context) error {
	ctx := c.Request().Context()
	form, _ := c.FormValues()
	values := map[string]string{}
	for _, f := range conf.Section.Fields {
		vs, ok := form[f.Key]
		if !ok || len(vs) == 0 {
			continue
		}
		if f.Kind == settings.Secret && vs[0] == "" {
			if form.Get("clear."+f.Key) == "1" {
				values[f.Key] = ""
			}
			continue
		}
		values[f.Key] = vs[0]
	}
	if err := h.Settings.Set(ctx, events.ActorAdmin, conf.Section.Name, values); err != nil {
		if fe, ok := err.(settings.FieldErrors); ok {
			return h.renderSettings(c, http.StatusUnprocessableEntity, values, fe, false)
		}
		return err
	}
	return web.Redirect(c, "/settings/routing?saved=1")
}

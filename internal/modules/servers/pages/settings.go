package pages

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// settingsPages is the side menu of Settings, for the module's own pages.
func (h *handler) settingsPages() []ui.SettingsPage {
	if h.SettingsPages == nil {
		return nil
	}
	return h.SettingsPages()
}

type serversSettingsView struct {
	Values  []settings.Value
	Errs    settings.FieldErrors
	Saved   bool
	Warning string // translated; "" when the hostname pattern's zone is allowed
}

func (h *handler) serversSettings(c *echo.Context) error {
	return h.renderServersSettings(c, http.StatusOK, nil, nil, c.QueryParam("saved") == "1")
}

// zoneWarning says so when the hostname pattern lands in a zone the admin has
// not ticked: the form would refuse every server.
func (h *handler) zoneWarning(ctx context.Context) (string, error) {
	pattern, err := h.Settings.Get(ctx, conf.HostnamePatternKey)
	if err != nil {
		return "", err
	}
	host, err := render.Hostname(pattern, "xx", 1)
	if err != nil {
		return "", nil // the field's own validation reports it
	}
	_, ok, err := h.DNS.Covers(ctx, host)
	if err != nil || ok {
		return "", err
	}
	return i18n.T(ctx, "servers.settings.zone_warning", i18n.Args{"host": host}), nil
}

func (h *handler) renderServersSettings(c *echo.Context, status int, submitted map[string]string, errs settings.FieldErrors, saved bool) error {
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
	warn, err := h.zoneWarning(ctx)
	if err != nil {
		return err
	}
	s := h.shell(c, i18n.T(ctx, "settings.servers"), "/settings")
	return web.Render(c, status, serversSettingsPage(s, h.settingsPages(), conf.Section, serversSettingsView{Values: vals, Errs: errs, Saved: saved, Warning: warn}))
}

func (h *handler) saveServersSettings(c *echo.Context) error {
	ctx := c.Request().Context()
	form, _ := c.FormValues()
	values := map[string]string{}
	for _, f := range conf.Section.Fields {
		if vs, ok := form[f.Key]; ok && len(vs) > 0 {
			values[f.Key] = vs[0]
		}
	}
	if err := h.Settings.Set(ctx, "admin", "servers", values); err != nil {
		if fe, ok := err.(settings.FieldErrors); ok {
			return h.renderServersSettings(c, http.StatusUnprocessableEntity, values, fe, false)
		}
		return err
	}
	return web.Redirect(c, "/settings/servers?saved=1")
}

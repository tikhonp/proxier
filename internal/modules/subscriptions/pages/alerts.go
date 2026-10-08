package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/alerts"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// flag is a country code's flag, "" for none (pages don't import servers'
// country package: ports only).
func flag(code string) string {
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	const base = 0x1F1E6 - 'A'
	return string([]rune{base + rune(code[0]), base + rune(code[1])})
}

// countryText is "🇳🇱 NL", or "—" when unknown.
func countryText(code string) string {
	if code == "" {
		return "—"
	}
	if f := flag(code); f != "" {
		return f + " " + code
	}
	return code
}

type countRow struct {
	Key, Country string
	Fetches      int
}

// whoView is the link page's "Who fetches it" and the alert band's counts.
type whoView struct {
	Week               bool // the 7 d switch
	Networks, Apps     []countRow
	Alert              bool
	AlertAt, AlertText string
	Limits             string // the Link area's "Alert limits" line
	Muted              bool
}

func (h *handler) whoView(ctx context.Context, l links.Link, week bool) (whoView, error) {
	loc := h.loc(ctx)
	now := h.Now()
	v := whoView{Week: week, Muted: l.AlertsMuted}
	since := now.Add(-alerts.Window)
	if week {
		since = now.Add(-7 * 24 * time.Hour)
	}
	counts, err := h.Alerts.Counts(ctx, l.ID, since)
	if err != nil {
		return v, err
	}
	for _, c := range counts.Networks {
		v.Networks = append(v.Networks, countRow{Key: c.Key, Country: countryText(c.Country), Fetches: c.Fetches})
	}
	for _, c := range counts.Apps {
		v.Apps = append(v.Apps, countRow{Key: appKeyName(loc, c.Key), Fetches: c.Fetches})
	}
	lim, err := h.Alerts.Limits(ctx, l)
	if err != nil {
		return v, err
	}
	args := i18n.Args{"networks": lim.Networks, "apps": lim.Apps}
	switch {
	case lim.Muted:
		v.Limits = loc.T("alerts.limits.muted")
	case lim.Default:
		v.Limits = loc.T("alerts.limits.default", args)
	default:
		v.Limits = loc.T("alerts.limits.own", args)
	}
	if !h.Alerts.HasAlert(l, now) {
		return v, nil
	}
	// the band counts the last 24 hours as they are now
	day := counts
	if week {
		if day, err = h.Alerts.Counts(ctx, l.ID, now.Add(-alerts.Window)); err != nil {
			return v, err
		}
	}
	v.Alert = true
	v.AlertAt = loc.Ago(l.AlertedAt) + " · " + loc.Clock(l.AlertedAt)[:5]
	v.AlertText = loc.T("alerts.band.text", i18n.Args{
		"networks": loc.N("alerts.n_networks", int64(len(day.Networks))), "apps": loc.N("alerts.n_apps", int64(len(day.Apps))),
		"max_networks": lim.Networks, "max_apps": lim.Apps,
	})
	return v, nil
}

// appKeyName shows an app count's key: the family, or "Other: <user agent>".
func appKeyName(loc *i18n.Localizer, key string) string {
	if ua, ok := strings.CutPrefix(key, "ua:"); ok {
		return appName(loc, "", ua)
	}
	return key
}

// ------------------------------------------------- raise limits, mute

type alertsFormView struct {
	L                    links.Link
	Networks, Apps       string
	DefNetworks, DefApps int
	Muted                bool
	Errs                 map[string]string
}

func (h *handler) alertsForm(c *echo.Context) (alertsFormView, error) {
	ctx := c.Request().Context()
	l, err := h.loadLink(c)
	if err != nil {
		return alertsFormView{}, err
	}
	if l.State == "deleted" {
		return alertsFormView{}, actionErr(links.ErrDeleted)
	}
	v := alertsFormView{L: l, Muted: l.AlertsMuted}
	if l.AlertNetworks != 0 {
		v.Networks = strconv.Itoa(l.AlertNetworks)
	}
	if l.AlertApps != 0 {
		v.Apps = strconv.Itoa(l.AlertApps)
	}
	def, err := h.Alerts.Limits(ctx, links.Link{})
	if err != nil {
		return v, err
	}
	v.DefNetworks, v.DefApps = def.Networks, def.Apps
	return v, nil
}

func (h *handler) alertsPage(c *echo.Context) error {
	v, err := h.alertsForm(c)
	if err != nil {
		return err
	}
	return h.renderAlerts(c, http.StatusOK, v)
}

func (h *handler) renderAlerts(c *echo.Context, status int, v alertsFormView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, alertsFormPage(h.shell(c, i18n.T(ctx, "alerts.form.title", i18n.Args{"name": v.L.Name}), "/links"), v))
}

// limitValue reads a limit field: empty is 0 (the setting); anything that
// isn't a number is out of bounds, so the service names it.
func limitValue(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return -1
	}
	return n
}

// saveAlerts: action=mute and action=unmute keep the limits (the band's
// Mute alerts, the page's Unmute); otherwise the form sets both limits and
// the mute.
func (h *handler) saveAlerts(c *echo.Context) error {
	v, err := h.alertsForm(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	networks, apps := v.L.AlertNetworks, v.L.AlertApps
	var muted bool
	switch c.FormValue("action") {
	case "mute":
		muted = true
	case "unmute":
	default:
		v.Networks, v.Apps, v.Muted = c.FormValue("networks"), c.FormValue("apps"), c.FormValue("muted") == "1"
		networks, apps, muted = limitValue(v.Networks), limitValue(v.Apps), v.Muted
	}
	err = h.Alerts.SetLimits(ctx, v.L.ID, networks, apps, muted, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderAlerts(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return actionErr(err)
	}
	return web.Redirect(c, linkHref(v.L.ID))
}

package pages

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ruleGroup is the rules of one module.
type ruleGroup struct {
	Module string
	Rules  []notify.Rule
}

type notificationsView struct {
	Groups     []ruleGroup
	OnlyChange bool
	Total      int
	Status     telegramState
}

func (h *handler) notificationsPage(c *echo.Context) error {
	ctx := c.Request().Context()
	rules, err := h.Notify.Rules(ctx)
	if err != nil {
		return err
	}
	only := c.QueryParam("show") == "changed"
	v := notificationsView{OnlyChange: only, Total: len(rules)}
	if v.Status, err = h.telegramState(ctx); err != nil {
		return err
	}
	by := map[string][]notify.Rule{}
	for _, r := range rules {
		if only && r.Enabled == r.Default {
			continue
		}
		by[r.Type.Module] = append(by[r.Type.Module], r)
	}
	mods := make([]string, 0, len(by))
	for m := range by {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	for _, m := range mods {
		v.Groups = append(v.Groups, ruleGroup{Module: m, Rules: by[m]})
	}
	s := h.shell(c, i18n.T(ctx, "settings.notifications"), "/settings")
	return web.Render(c, http.StatusOK, notificationsPage(s, h.sortedPages(), v))
}

// notificationsSet turns one event type's rule on or off.
func (h *handler) notificationsSet(c *echo.Context) error {
	ctx := c.Request().Context()
	var on bool
	switch c.FormValue("enabled") {
	case "1":
		on = true
	case "0":
	default:
		return echo.ErrBadRequest
	}
	err := h.Notify.SetRule(ctx, c.Param("type"), on)
	if errors.Is(err, notify.ErrUnknownRule) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	to := "/settings/notifications"
	if c.FormValue("show") == "changed" {
		to += "?show=changed"
	}
	return web.Redirect(c, to)
}

// notificationRetry sends a failed notification again.
func (h *handler) notificationRetry(c *echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	if err := h.Notify.Retry(c.Request().Context(), id); err != nil && !errors.Is(err, notify.ErrNotFailed) {
		return err
	}
	return web.Redirect(c, "/")
}

// failedSince is the dashboard's window.
const failedWindow = 7 * 24 * time.Hour

func chatIDString(id int64) string { return strconv.FormatInt(id, 10) }

// moduleLabel names a group of rules; a module without a translation shows its name.
func moduleLabel(ctx context.Context, module string) string {
	return tr(ctx, "notifications.module."+module, module)
}

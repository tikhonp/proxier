package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func linkHref(id int64) string { return "/links/" + i64(id) }

// statusKind is the marker of a link's shown status.
func statusKind(status string) string {
	return map[string]string{"active": "ok", "disabled": "off", "expired": "off", "deleted": "gone"}[status]
}

// loc is the request's localizer on the module's clock, so "ago" agrees
// with the times the module records.
func (h *handler) loc(ctx context.Context) *i18n.Localizer { return i18n.From(ctx).WithNow(h.Now) }

// expiryShort is the list's expiry: "8 Oct · in 3 days", "expired 30 Sep", "never".
func expiryShort(loc *i18n.Localizer, l links.Link, now time.Time) string {
	if l.Expires.IsZero() {
		return loc.T("links.expiry.never")
	}
	day := lastDay(loc, l.Expires, false)
	if l.Expired(now) {
		return loc.T("links.expiry.expired", i18n.Args{"date": day})
	}
	left := l.Expires.Sub(now)
	var in string
	if left < 24*time.Hour {
		in = loc.N("links.in_hours", int64(left/time.Hour))
	} else {
		in = loc.N("links.in_days", int64(left/(24*time.Hour)))
	}
	return day + " · " + in
}

// lastDay is the last moment a link works: "1 Dec 2026" for a date-only
// expiry (the day before its midnight), else "1 Dec 2026, 18:00".
func lastDay(loc *i18n.Localizer, expires time.Time, year bool) string {
	date := loc.ShortDate
	if year {
		date = loc.Date
	}
	if links.DateOnly(expires, loc.TZ) {
		return date(expires.Add(-time.Second))
	}
	return date(expires) + ", " + expires.In(loc.TZ).Format("15:04")
}

// expiryLong is the link page's "until 1 Dec 2026" or "no expiry".
func expiryLong(loc *i18n.Localizer, l links.Link) string {
	if l.Expires.IsZero() {
		return loc.T("links.expiry.none")
	}
	return loc.T("links.expiry.until", i18n.Args{"date": lastDay(loc, l.Expires, true)})
}

func appName(loc *i18n.Localizer, app, ua string) string {
	if app != "" {
		return app
	}
	if ua == "" {
		return loc.T("links.app.other")
	}
	return loc.T("links.app.other_ua", i18n.Args{"ua": ua})
}

func lastFetch(loc *i18n.Localizer, l links.Link) string {
	if l.LastFetchAt.IsZero() {
		return loc.T("links.fetch.never")
	}
	return loc.Ago(l.LastFetchAt) + " · " + appName(loc, l.LastFetchApp, "")
}

// ---------------------------------------------------------------- the list

type linkRow struct {
	ID                 int64
	Name, Subscription string
	Kind, Word         string
	Expires, Last      string
	Fetches            int
	Alert              bool
}

type linksView struct {
	Line      string
	Rows      []linkRow
	Name      string
	SubID     string
	State     string
	Expiring  bool
	Alerts    bool
	Subs      []ui.FilterOption
	States    []ui.FilterOption
	ChipHref  string
	AlertHref string
	ClearHref string
	Tombs     int
	TombsHref string
	Empty     bool // no link at all
}

func filterURL(name, sub, state string, expiring, alerts bool) string {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	if sub != "" && sub != "0" {
		q.Set("subscription", sub)
	}
	if state != "" && state != "live" {
		q.Set("state", state)
	}
	if expiring {
		q.Set("expiring", "1")
	}
	if alerts {
		q.Set("alerts", "1")
	}
	if len(q) == 0 {
		return "/links"
	}
	return "/links?" + q.Encode()
}

func (h *handler) linkList(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := h.loc(ctx)
	now := h.Now()
	v := linksView{Name: c.QueryParam("name"), SubID: c.QueryParam("subscription"), State: c.QueryParam("state"), Expiring: c.QueryParam("expiring") == "1", Alerts: c.QueryParam("alerts") == "1"}
	switch v.State {
	case "live", "active", "disabled", "expired", "deleted", "all":
	default:
		v.State = "live"
	}
	subID, _ := strconv.ParseInt(v.SubID, 10, 64)
	f := links.Filter{Name: v.Name, SubscriptionID: subID, State: v.State}
	if v.Expiring {
		f.ExpiringWithin = 7 * 24 * time.Hour
	}
	rows, err := h.Links.List(ctx, f)
	if err != nil {
		return err
	}
	all, err := h.Links.List(ctx, links.Filter{State: "all"})
	if err != nil {
		return err
	}
	period, err := h.Links.Tombstone(ctx)
	if err != nil {
		return err
	}
	active, expiring, alerted := 0, 0, 0
	for _, r := range all {
		if r.State != "deleted" && h.Alerts.HasAlert(r.Link, now) {
			alerted++
		}
		switch st := r.Status(now); {
		case st == "active":
			active++
			if !r.Expires.IsZero() && r.Expires.Sub(now) <= 7*24*time.Hour {
				expiring++
			}
		case st == "deleted" && r.SubscriptionID != 0 && now.Before(r.TombstoneEnds(period)):
			v.Tombs++
		}
	}
	v.Empty = len(all) == 0
	v.Line = loc.N("links.line.active", int64(active)) + " · " + loc.N("links.line.expiring", int64(expiring))
	if alerted > 0 {
		v.Line += " · " + loc.N("links.line.alerts", int64(alerted))
	}
	if v.State == "deleted" {
		v.Tombs = 0
	}
	v.TombsHref = "/links?state=deleted"
	for _, r := range rows {
		alert := h.Alerts.HasAlert(r.Link, now)
		if v.Alerts && !alert {
			continue
		}
		st := r.Status(now)
		v.Rows = append(v.Rows, linkRow{
			ID: r.ID, Name: r.Name, Subscription: r.Subscription, Kind: statusKind(st), Word: loc.T("links.status." + st),
			Expires: expiryShort(loc, r.Link, now), Last: lastFetch(loc, r.Link), Fetches: r.Fetches24h, Alert: alert,
		})
	}
	subList, err := h.Subs.List(ctx)
	if err != nil {
		return err
	}
	v.Subs = []ui.FilterOption{{Value: "", Label: loc.T("links.filter.sub_all")}}
	for _, s := range subList {
		v.Subs = append(v.Subs, ui.FilterOption{Value: i64(s.ID), Label: s.Name})
	}
	for _, st := range []string{"live", "active", "disabled", "expired", "deleted", "all"} {
		v.States = append(v.States, ui.FilterOption{Value: st, Label: loc.T("links.filter.state." + st)})
	}
	v.ChipHref = filterURL(v.Name, v.SubID, v.State, !v.Expiring, v.Alerts)
	v.AlertHref = filterURL(v.Name, v.SubID, v.State, v.Expiring, !v.Alerts)
	if v.Name != "" || subID != 0 || v.State != "live" || v.Expiring || v.Alerts {
		v.ClearHref = "/links"
	}
	return web.Render(c, http.StatusOK, linksPage(h.shell(c, loc.T("links.title"), "/links"), v))
}

// ------------------------------------------------------------- new link

type expiryForm struct {
	Mode, Date, Clock string
	Zone              string
}

type newLinkView struct {
	Name, Note string
	SubID      int64
	Subs       []subs.Row
	Expiry     expiryForm
	Lang       string
	Errs       map[string]string
}

func (h *handler) zoneName(ctx context.Context) string { return h.Links.Zone(ctx).String() }

func (h *handler) newLinkPage(c *echo.Context) error {
	ctx := c.Request().Context()
	v := newLinkView{Lang: string(h.Links.DefaultLang(ctx)), Expiry: expiryForm{Mode: "never", Zone: h.zoneName(ctx)}}
	v.SubID, _ = strconv.ParseInt(c.QueryParam("subscription"), 10, 64)
	return h.renderNewLink(c, http.StatusOK, v)
}

func (h *handler) renderNewLink(c *echo.Context, status int, v newLinkView) error {
	ctx := c.Request().Context()
	list, err := h.Subs.List(ctx)
	if err != nil {
		return err
	}
	v.Subs = list
	if len(list) == 1 {
		v.SubID = list[0].ID
	}
	return web.Render(c, status, newLinkPage(h.shell(c, i18n.T(ctx, "links.new"), "/links"), v))
}

func (h *handler) createLink(c *echo.Context) error {
	ctx := c.Request().Context()
	v := newLinkView{
		Name: c.FormValue("name"), Note: c.FormValue("note"), Lang: c.FormValue("language"),
		Expiry: expiryForm{Mode: c.FormValue("expiry"), Date: c.FormValue("expiry_date"), Clock: c.FormValue("expiry_time"), Zone: h.zoneName(ctx)},
	}
	v.SubID, _ = strconv.ParseInt(c.FormValue("subscription"), 10, 64)
	// an expiry that doesn't parse stops here: Create would take it as "never"
	expires, err := links.ParseExpiry(v.Expiry.Mode, v.Expiry.Date, v.Expiry.Clock, h.Links.Zone(ctx), h.Now())
	var id int64
	if err == nil {
		id, err = h.Links.Create(ctx, links.New{Name: v.Name, Note: v.Note, SubscriptionID: v.SubID, Expires: expires, Lang: i18n.Lang(v.Lang)}, events.ActorAdmin)
	}
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderNewLink(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, linkHref(id)+"?created=1")
}

// ---------------------------------------------------------------- helpers

// loadLink reads the link of the :id parameter; 404 when there is none.
func (h *handler) loadLink(c *echo.Context) (links.Link, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return links.Link{}, echo.ErrNotFound
	}
	l, err := h.Links.Get(c.Request().Context(), id)
	if errors.Is(err, links.ErrNotFound) {
		return l, echo.ErrNotFound
	}
	return l, err
}

// actionErr maps the service's errors to answers: a deleted link is read-only.
func actionErr(err error) error {
	switch {
	case errors.Is(err, links.ErrNotFound):
		return echo.ErrNotFound
	case errors.Is(err, links.ErrDeleted):
		return echo.NewHTTPError(http.StatusConflict, "the link is deleted")
	}
	return err
}

// maskedURL is the URL with the token as 22 bullets.
func (h *handler) maskedURL() string {
	return h.Links.URL("") + strings.Repeat("•", 22)
}

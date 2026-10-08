package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// fetchPage is how many fetches the link page shows at once.
const fetchPage = 50

type fetchRow struct {
	Time, IP, Country, App, Format, Outcome string
	OK                                      bool
}

type linkView struct {
	L          links.Link
	Kind, Word string
	Line       string
	Band       string // created, regenerated, disabled, expired, deleted, ended
	BandText   string
	URL        string // "" for a deleted link
	Masked     string
	SubName    string
	SubServers string
	Expiry     string
	Lang       string
	Format     string
	LastFetch  string
	Created    string
	Preview    previewView
	HasPreview bool
	Retention  string
	Fetches    []fetchRow
	OlderHref  string
	Activity   []eventLine
	Deleted    bool
	Who        whoView
	// CanCutOff: the servers module rotates; CutOffBand is the alert band's
	// line about it, CutOff the latest cut-off (HasCutOff).
	CanCutOff  bool
	CutOffBand string
	CutOff     cutoffView
	HasCutOff  bool
}

func (h *handler) linkView(c *echo.Context, l links.Link) (linkView, error) {
	ctx := c.Request().Context()
	loc := h.loc(ctx)
	now := h.Now()
	st := l.Status(now)
	v := linkView{L: l, Kind: statusKind(st), Word: loc.T("links.status." + st), Deleted: l.State == "deleted"}
	v.Expiry = expiryLong(loc, l)
	v.Lang = loc.T("links.lang." + string(l.Lang))
	v.Created = loc.ShortDate(l.CreatedAt)
	if l.LastFetchAt.IsZero() {
		v.LastFetch = loc.T("links.fetch.never")
	} else {
		v.LastFetch = loc.Ago(l.LastFetchAt) + " · " + appName(loc, l.LastFetchApp, "") + " · " + l.LastFetchNetwork
	}
	if l.SubscriptionID != 0 {
		sub, err := h.Subs.Get(ctx, l.SubscriptionID)
		if err != nil {
			return v, err
		}
		v.SubName = sub.Name
		members, err := h.Subs.Members(ctx, sub.ID)
		if err != nil {
			return v, err
		}
		var names []string
		for _, m := range members {
			names = append(names, m.Name)
		}
		v.SubServers = strings.Join(names, ", ")
		v.Format = sub.DefaultFormat + " " + loc.T("links.format.default_mark")
		if l.Format != "" {
			v.Format = l.Format
		}
	}
	v.Line = v.SubName + " · " + v.Expiry + " · " + v.Lang + " · " + loc.T("links.created_on", i18n.Args{"date": v.Created})

	period, err := h.Links.Tombstone(ctx)
	if err != nil {
		return v, err
	}
	ended, err := h.Links.Ended(ctx, l)
	if err != nil {
		return v, err
	}
	switch {
	case v.Deleted && ended:
		v.Band, v.BandText = "ended", loc.T("links.band.ended", i18n.Args{"date": loc.Date(l.DeletedAt), "end": loc.Date(l.TombstoneEnds(period))})
	case v.Deleted:
		v.Band, v.BandText = "deleted", loc.T("links.band.deleted", i18n.Args{"date": loc.Date(l.DeletedAt), "end": loc.Date(l.TombstoneEnds(period))})
	case c.QueryParam("created") == "1":
		v.Band, v.BandText = "created", loc.T("links.band.created")
	case c.QueryParam("regenerated") == "1":
		v.Band, v.BandText = "regenerated", loc.T("links.band.regenerated")
	case st == "disabled":
		v.Band, v.BandText = "disabled", loc.T("links.band.disabled")
	case st == "expired":
		v.Band, v.BandText = "expired", loc.T("links.band.expired", i18n.Args{"date": lastDay(loc, l.Expires, true)})
	}
	if !v.Deleted {
		token, err := h.Links.Token(ctx, l.ID)
		if err != nil {
			return v, err
		}
		v.URL, v.Masked = h.Links.URL(token), h.maskedURL()
	}
	if !ended {
		resp, format, err := h.Links.Serve(ctx, l, "", true)
		if err != nil {
			return v, err
		}
		v.Preview, v.HasPreview = responseView(ctx, resp, format), true
	}

	if v.Who, err = h.whoView(ctx, l, c.QueryParam("window") == "7d"); err != nil {
		return v, err
	}
	if v.Deleted {
		v.Who.Alert = false
	}
	v.CanCutOff = h.Links.CanCutOff() && !v.Deleted
	if v.CanCutOff && v.Who.Alert {
		if v.CutOffBand, err = h.cutoffBand(ctx, l); err != nil {
			return v, err
		}
	}
	if h.Links.CanCutOff() {
		if v.CutOff, v.HasCutOff, err = h.cutoffView(ctx, l.ID); err != nil {
			return v, err
		}
	}

	retention, err := h.Settings.GetDuration(ctx, conf.FetchRetention)
	if err != nil {
		return v, err
	}
	v.Retention = loc.N("links.fetchlog.kept", int64(retention/(24*time.Hour)))
	before, _ := strconv.ParseInt(c.QueryParam("before"), 10, 64)
	fetches, err := h.Links.Fetches(ctx, l.ID, before, fetchPage+1)
	if err != nil {
		return v, err
	}
	if len(fetches) > fetchPage {
		fetches = fetches[:fetchPage]
		v.OlderHref = linkHref(l.ID) + "?before=" + i64(fetches[len(fetches)-1].ID)
	}
	for _, f := range fetches {
		v.Fetches = append(v.Fetches, fetchRow{
			Time: loc.Time(f.At.Time), IP: f.IP, Country: countryText(f.Country), App: appName(loc, f.App, f.UserAgent), Format: f.Format,
			Outcome: loc.T("links.outcome." + f.Outcome), OK: f.Outcome == output.OK,
		})
	}
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: links.Subject(l.ID), Limit: 10})
	if err != nil {
		return v, err
	}
	for _, e := range list {
		args := i18n.Args{"subject": l.Name, "actor": e.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		text := e.Type
		if key := "event." + e.Type; loc.Has(key) {
			text = loc.T(key, args)
		}
		v.Activity = append(v.Activity, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return v, nil
}

// responseView is the preview text of what a link gets: status, headers,
// a blank line, the lines (masked).
func responseView(ctx context.Context, resp output.Response, format string) previewView {
	v := previewView{Format: format, Status: "200 OK"}
	v.Headers = append([]output.Header{
		{Name: "content-type", Value: "text/plain; charset=utf-8"},
		{Name: "cache-control", Value: "no-store"},
	}, resp.Headers...)
	var b strings.Builder
	b.WriteString(v.Status + "\n")
	for _, hd := range v.Headers {
		b.WriteString(hd.Name + ": " + hd.Value + "\n")
	}
	b.WriteString("\n")
	for _, l := range resp.Lines {
		b.WriteString(l + "\n")
	}
	v.Text = b.String()
	v.Base64 = format == "uri-base64"
	loc := i18n.From(ctx)
	for _, hd := range resp.Hidden {
		v.Notes = append(v.Notes, loc.T("links.preview.hidden", i18n.Args{"name": hd.Server.Name, "state": loc.T("subs.state." + hd.Server.Health)}))
	}
	v.AllHidden = resp.AllHidden
	return v
}

func (h *handler) linkPage(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	v, err := h.linkView(c, l)
	if err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, linkPage(h.shell(c, l.Name, "/links"), v))
}

// linkURL is the Reveal fragment: the URL in clear, or masked with ?hide=1.
func (h *handler) linkURL(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	token, err := h.Links.Token(c.Request().Context(), l.ID)
	if err != nil {
		return actionErr(err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, urlField(l.ID, h.Links.URL(token), h.maskedURL(), c.QueryParam("hide") != "1"))
}

// linkQR is the QR fragment, or the Show QR button again with ?hide=1.
func (h *handler) linkQR(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	token, err := h.Links.Token(c.Request().Context(), l.ID)
	if err != nil {
		return actionErr(err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, qrSlot(l.ID, l.Name, h.Links.URL(token), c.QueryParam("hide") != "1"))
}

// act runs a one-click action and goes back to the link page.
func (h *handler) act(fn func(ctx context.Context, id int64, actor string) error, query string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		l, err := h.loadLink(c)
		if err != nil {
			return err
		}
		if err := fn(c.Request().Context(), l.ID, events.ActorAdmin); err != nil {
			return actionErr(err)
		}
		return web.Redirect(c, linkHref(l.ID)+query)
	}
}

func (h *handler) deleteLink(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	if err := h.Links.Delete(c.Request().Context(), l.ID, events.ActorAdmin); err != nil {
		return actionErr(err)
	}
	return web.Redirect(c, "/links")
}

// ------------------------------------------------- pages styled as dialogs

type linkFormView struct {
	L       links.Link
	Kind    string // subscription, expiry, edit
	Subs    []subs.Row
	SubID   int64
	Expiry  expiryForm
	Hint    string
	Name    string
	Note    string
	Lang    string
	Format  string
	Formats []string
	Default string
	Errs    map[string]string
}

func (h *handler) linkForm(c *echo.Context, kind string) (linkFormView, error) {
	ctx := c.Request().Context()
	l, err := h.loadLink(c)
	if err != nil {
		return linkFormView{}, err
	}
	if l.State == "deleted" {
		return linkFormView{}, actionErr(links.ErrDeleted)
	}
	loc := h.loc(ctx)
	v := linkFormView{L: l, Kind: kind, SubID: l.SubscriptionID, Name: l.Name, Note: l.Note, Lang: string(l.Lang), Format: l.Format}
	v.Expiry = expiryForm{Mode: "never", Zone: h.zoneName(ctx)}
	if !l.Expires.IsZero() {
		v.Expiry.Mode = "on"
		zone := h.Links.Zone(ctx)
		if links.DateOnly(l.Expires, zone) {
			v.Expiry.Date = l.Expires.Add(-time.Second).In(zone).Format("2006-01-02")
		} else {
			v.Expiry.Date, v.Expiry.Clock = l.Expires.In(zone).Format("2006-01-02"), l.Expires.In(zone).Format("15:04")
		}
		v.Hint = loc.T("links.expiry.works_until", i18n.Args{"date": lastDay(loc, l.Expires, true), "zone": v.Expiry.Zone})
	}
	list, err := h.Subs.List(ctx)
	if err != nil {
		return v, err
	}
	for _, s := range list {
		if s.ID == l.SubscriptionID {
			v.Formats, v.Default = s.Formats, s.DefaultFormat
			continue
		}
		v.Subs = append(v.Subs, s)
	}
	return v, nil
}

func (h *handler) renderLinkForm(c *echo.Context, status int, v linkFormView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, linkFormPage(h.shell(c, i18n.T(ctx, "links.form."+v.Kind+".title", i18n.Args{"name": v.L.Name}), "/links"), v))
}

func (h *handler) formPage(kind string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		v, err := h.linkForm(c, kind)
		if err != nil {
			return err
		}
		return h.renderLinkForm(c, http.StatusOK, v)
	}
}

func (h *handler) changeSubscription(c *echo.Context) error {
	v, err := h.linkForm(c, "subscription")
	if err != nil {
		return err
	}
	to, _ := strconv.ParseInt(c.FormValue("subscription"), 10, 64)
	err = h.Links.ChangeSubscription(c.Request().Context(), v.L.ID, to, events.ActorAdmin)
	if errors.Is(err, subs.ErrNotFound) {
		v.Errs = map[string]string{"subscription": i18n.T(c.Request().Context(), "links.err.subscription")}
		return h.renderLinkForm(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return actionErr(err)
	}
	return web.Redirect(c, linkHref(v.L.ID))
}

func (h *handler) setExpiry(c *echo.Context) error {
	v, err := h.linkForm(c, "expiry")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v.Expiry = expiryForm{Mode: c.FormValue("expiry"), Date: c.FormValue("expiry_date"), Clock: c.FormValue("expiry_time"), Zone: v.Expiry.Zone}
	expires, err := links.ParseExpiry(v.Expiry.Mode, v.Expiry.Date, v.Expiry.Clock, h.Links.Zone(ctx), h.Now())
	if err == nil {
		err = h.Links.SetExpiry(ctx, v.L.ID, expires, events.ActorAdmin)
	}
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderLinkForm(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return actionErr(err)
	}
	return web.Redirect(c, linkHref(v.L.ID))
}

func (h *handler) editLink(c *echo.Context) error {
	v, err := h.linkForm(c, "edit")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v.Name, v.Note, v.Lang, v.Format = c.FormValue("name"), c.FormValue("note"), c.FormValue("language"), c.FormValue("format")
	err = h.Links.Edit(ctx, v.L.ID, links.Edit{Name: v.Name, Note: v.Note, Lang: i18n.Lang(v.Lang), Format: v.Format}, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderLinkForm(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return actionErr(err)
	}
	return web.Redirect(c, linkHref(v.L.ID))
}

// moveLinks moves every live link of the subscription to another one, from
// its delete page.
func (h *handler) moveLinks(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	to, _ := strconv.ParseInt(c.FormValue("to"), 10, 64)
	if _, err := h.Links.MoveAll(ctx, s.ID, to, events.ActorAdmin); errors.Is(err, subs.ErrNotFound) {
		v, verr := h.deleteView(ctx, s)
		if verr != nil {
			return verr
		}
		v.Err = i18n.T(ctx, "links.err.subscription")
		return h.renderDelete(c, http.StatusUnprocessableEntity, v)
	} else if err != nil {
		return err
	}
	return web.Redirect(c, subHref(s.ID)+"/delete")
}

// ----------------------------------------------- the subscription's links

type subLinkRow struct {
	ID               int64
	Name, Kind, Word string
	Last             string
}

func (h *handler) subLinks(ctx context.Context, subID int64) ([]subLinkRow, error) {
	list, err := h.Links.OfSubscription(ctx, subID)
	if err != nil {
		return nil, err
	}
	loc := h.loc(ctx)
	now := h.Now()
	var out []subLinkRow
	for _, l := range list {
		st := l.Status(now)
		out = append(out, subLinkRow{ID: l.ID, Name: l.Name, Kind: statusKind(st), Word: loc.T("links.status." + st), Last: lastFetch(loc, l)})
	}
	return out, nil
}

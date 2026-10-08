package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/refresh"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// bandNames is how many removed and added names the rejected band shows.
const bandNames = 20

// sourceView is the Source area: the selector's facts, the last check and,
// while a refresh is queued or running, the poll.
type sourceView struct {
	ID    int64
	Rows  []kv
	Check string
	Bad   bool  // the check line is a failure
	Job   int64 // the active refresh: the area polls
}

// rejectedView is the band of a waiting snapshot.
type rejectedView struct {
	SnapID                 int64
	Title, Line            string
	RemovedHead, AddedHead string
	Removed, Added         []diffLine
	MoreRemoved, MoreAdded string // "… N more"
	DiffHref               string
	Keep                   string
	AcceptTitle            string
	AcceptBody             string
	Footer                 string
	Cause                  string // "Likely cause: …", "" for none
	CauseSel, CauseHref    string
}

// stateKind maps a service state to a status marker.
func stateKind(state string) string {
	switch state {
	case store.StateWaiting:
		return "look"
	case store.StateFailing:
		return "broken"
	}
	return "ok"
}

func (h *handler) sourceOf(ctx context.Context, it services.Item, acc services.Snapshot) (sourceView, error) {
	loc := i18n.From(ctx)
	ms, err := h.Lists.Memberships(ctx, it.ID)
	if err != nil {
		return sourceView{}, err
	}
	v := sourceView{ID: it.ID, Rows: sourceRows(ctx, it, acc, ms)}
	if it.Source == selector.Custom {
		return v, nil
	}
	switch {
	case it.Failures > 0:
		v.Bad = true
		v.Check = loc.N("refresh.check.failing", int64(it.Failures), i18n.Args{"error": it.LastError})
		if !it.LastChecked.IsZero() {
			v.Check += " · " + i18n.T(ctx, "refresh.check.last_good", i18n.Args{"time": loc.Time(it.LastChecked)})
		}
	case it.LastChecked.IsZero():
		v.Check = i18n.T(ctx, "refresh.check.never")
	default:
		v.Check = i18n.T(ctx, "refresh.check.ok", i18n.Args{"time": loc.Time(it.LastChecked)})
	}
	if v.Job, err = h.Refresh.ActiveJob(ctx, it.ID); err != nil {
		return v, err
	}
	return v, nil
}

// rejectedOf is the waiting snapshot's band, nil when nothing waits.
func (h *handler) rejectedOf(ctx context.Context, it services.Item, acc services.Snapshot) (*rejectedView, error) {
	w, ok, err := h.Refresh.Waiting(ctx, it.ID)
	if err != nil || !ok {
		return nil, err
	}
	loc := i18n.From(ctx)
	now := h.Now()
	v := &rejectedView{SnapID: w.ID, DiffHref: svcHref(it.ID) + "/snapshots/" + i64(w.ID)}
	if loc.Day(w.FetchedAt) == loc.Day(now) {
		v.Title = i18n.T(ctx, "refresh.band.title_today")
	} else {
		v.Title = i18n.T(ctx, "refresh.band.title_day", i18n.Args{"date": loc.ShortDate(w.FetchedAt)})
	}
	old, cnt := int64(acc.Count()), int64(w.Count())
	if w.Reason == refresh.ReasonEmpty {
		v.Line = i18n.T(ctx, "refresh.band.empty")
	} else {
		pct, err := h.Settings.GetInt(ctx, "routing.shrink_pct")
		if err != nil {
			return nil, err
		}
		v.Line = loc.N("refresh.band.shrink", cnt, i18n.Args{"old": loc.Number(old), "pct": w.LostPct, "limit": pct})
	}
	d := snapshot.Compare(acc.Set, w.Set)
	v.RemovedHead = i18n.T(ctx, "refresh.band.removed", i18n.Args{"n": loc.Number(int64(d.Removed()))})
	v.AddedHead = i18n.T(ctx, "refresh.band.added", i18n.Args{"n": loc.Number(int64(d.Added()))})
	v.Removed, v.MoreRemoved = bandLines(ctx, d.RemovedSuffix, d.RemovedExact)
	v.Added, v.MoreAdded = bandLines(ctx, d.AddedSuffix, d.AddedExact)
	v.Keep = loc.N("refresh.band.keep", old)
	v.Footer = loc.N("refresh.band.footer", old)
	names, err := h.Services.ListsOf(ctx, it.ID)
	if err != nil {
		return nil, err
	}
	v.AcceptTitle = loc.N("refresh.accept.title", cnt, i18n.Args{"tag": it.Tag})
	switch {
	case len(names) == 0:
		v.AcceptBody = i18n.T(ctx, "refresh.accept.no_lists")
	case d.Removed() > 0:
		v.AcceptBody = loc.N("refresh.accept.lose", int64(d.Removed()), i18n.Args{"lists": andList(ctx, names)})
	default:
		v.AcceptBody = loc.N("refresh.accept.gain", int64(d.Added()), i18n.Args{"lists": andList(ctx, names)})
	}
	if err := h.likelyCause(ctx, it, append(append([]string(nil), d.RemovedSuffix...), d.RemovedExact...), func(line, sel, href string) {
		v.Cause, v.CauseSel, v.CauseHref = line, sel, href
	}); err != nil {
		return nil, err
	}
	return v, nil
}

func bandLines(ctx context.Context, suffix, exact []string) ([]diffLine, string) {
	all, _ := diffLines(ctx, suffix, exact)
	if len(all) > bandNames {
		return all[:bandNames], i18n.T(ctx, "services.diff.more", i18n.Args{"n": len(all) - bandNames})
	}
	return all, ""
}

// likelyCause looks the removed names up in the catalog: when one selector
// (not the service's own) holds at least half of them, it says so, with a
// link that adds that selector to the service's lists. Computed when the page
// renders, never stored; nothing for a URL or custom service.
func (h *handler) likelyCause(ctx context.Context, it services.Item, removed []string, set func(line, sel, href string)) error {
	if len(removed) == 0 || (it.Source != selector.V2fly && it.Source != selector.Iplist) {
		return nil
	}
	holders, at, err := h.Catalog.WhereAre(ctx, removed, it.Selector)
	if err != nil || len(holders) == 0 || holders[0].Names*2 < len(removed) {
		return err
	}
	loc := i18n.From(ctx)
	top := holders[0]
	ms, err := h.Lists.Memberships(ctx, it.ID)
	if err != nil {
		return err
	}
	q := url.Values{"selector": {top.Selector}}
	for _, m := range ms {
		q.Add("lists", i64(m.List.ID))
	}
	set(loc.N("refresh.cause", int64(len(removed)), i18n.Args{
		"held": loc.Number(int64(top.Names)), "selector": top.Selector, "date": loc.ShortDate(at),
	}), top.Selector, listPath+"/add?"+q.Encode())
	return nil
}

func (h *handler) refreshPost(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	id, err := h.Refresh.RefreshNow(c.Request().Context(), it.ID, events.ActorAdmin)
	if errors.Is(err, services.ErrCustom) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	if c.FormValue("from") == "list" {
		return web.Redirect(c, listPath+"?refreshed="+url.QueryEscape(it.Tag))
	}
	return web.Redirect(c, svcHref(it.ID)+"?refresh="+i64(id))
}

// sourceArea answers the Source area's poll. When the refresh it waited for
// has ended, the page reloads: the snapshot, the band and the state may have
// changed.
func (h *handler) sourceArea(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	if !htmx(c) {
		return web.Redirect(c, svcHref(it.ID))
	}
	ctx := c.Request().Context()
	acc, err := h.Services.Accepted(ctx, it.ID)
	if err != nil {
		return err
	}
	v, err := h.sourceOf(ctx, it, acc)
	if err != nil {
		return err
	}
	if v.Job == 0 && c.QueryParam("polling") == "1" {
		c.Response().Header().Set("HX-Refresh", "true")
	}
	return web.Render(c, http.StatusOK, sourceAreaView(v))
}

func (h *handler) snapParam(c *echo.Context) (int64, error) {
	n, err := strconv.ParseInt(c.Param("snap"), 10, 64)
	if err != nil {
		return 0, echo.ErrNotFound
	}
	return n, nil
}

func (h *handler) acceptPost(c *echo.Context) error {
	return h.decide(c, func(ctx context.Context, id, snap int64) error {
		return h.Refresh.Accept(ctx, id, snap, events.ActorAdmin)
	}, "accepted")
}

func (h *handler) dismissPost(c *echo.Context) error {
	return h.decide(c, func(ctx context.Context, id, snap int64) error {
		return h.Refresh.Dismiss(ctx, id, snap, events.ActorAdmin)
	}, "dismissed")
}

func (h *handler) decide(c *echo.Context, act func(ctx context.Context, id, snap int64) error, band string) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	snap, err := h.snapParam(c)
	if err != nil {
		return err
	}
	err = act(c.Request().Context(), it.ID, snap)
	if errors.Is(err, refresh.ErrNotWaiting) {
		return web.Redirect(c, svcHref(it.ID)+"?gone=1")
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, svcHref(it.ID)+"?"+band+"=1")
}

func (h *handler) refreshListPost(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	n, err := h.Refresh.RefreshList(c.Request().Context(), l.ID, events.ActorAdmin)
	if errors.Is(err, lists.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, listHref(l.ID)+"?refreshing="+strconv.Itoa(n))
}

// stateWord is a state as a word.
func stateWord(ctx context.Context, state string) string {
	if state == "" {
		state = store.StateOK
	}
	return i18n.T(ctx, "services.state."+state)
}

// refreshedAt is "04:00": the daily round's time from its setting.
func (h *handler) refreshedAt(ctx context.Context) string {
	v, err := h.Settings.Get(ctx, "routing.refresh_at")
	if err != nil {
		return ""
	}
	return v
}

// ageWord is "3 days old" when a catalog is older than 36 hours.
func ageWord(ctx context.Context, age time.Duration) string {
	if age <= 36*time.Hour {
		return ""
	}
	return i18n.N(ctx, "catalog.age", int64(age/(24*time.Hour)))
}

// actionFor is an Accept anyway button with its consequence dialog.
func acceptAction(id int64, v *rejectedView) ui.Action {
	return ui.Action{
		ID: "refresh.accept", Label: "refresh.accept", Method: "POST", Small: true,
		Href:    svcHref(id) + "/snapshots/" + i64(v.SnapID) + "/accept",
		Confirm: &ui.Confirm{Strength: 2, Title: v.AcceptTitle, Body: v.AcceptBody},
	}
}

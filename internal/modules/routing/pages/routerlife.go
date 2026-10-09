package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// registerRouterLife adds drift, unmanaged tags, pause and removal (3f).
func (h *handler) registerRouterLife(r web.Routes) {
	r.Admin.POST(routersPath+"/:id/repair", h.routerRepair)
	r.Admin.GET(routersPath+"/:id/unmanaged/:tag/adopt", h.adoptPage)
	r.Admin.POST(routersPath+"/:id/unmanaged/:tag/adopt", h.adoptPost)
	r.Admin.POST(routersPath+"/:id/unmanaged/:tag/remove", h.unmanagedRemove)
	r.Admin.POST(routersPath+"/:id/unmanaged/:tag/ignore", h.unmanagedIgnore(true))
	r.Admin.POST(routersPath+"/:id/unmanaged/:tag/unignore", h.unmanagedIgnore(false))
	r.Admin.POST(routersPath+"/:id/pause", h.routerPause)
	r.Admin.POST(routersPath+"/:id/resume", h.routerResume)
	r.Admin.GET(routersPath+"/:id/remove", h.routerRemovePage)
	r.Admin.POST(routersPath+"/:id/remove", h.routerRemovePost)
	r.Admin.POST(routersPath+"/:id/remove/keep", h.routerRemoveKeep)
	r.Admin.POST(routersPath+"/:id/remove/retry", h.routerRemoveRetry)
}

// unmanagedHref is an unmanaged tag's base path; tags may hold spaces.
func unmanagedHref(id int64, tag string) string {
	return routerHref(id) + "/unmanaged/" + url.PathEscape(tag)
}

func tagParam(c *echo.Context) string {
	t := c.Param("tag")
	if u, err := url.PathUnescape(t); err == nil {
		return u
	}
	return t
}

// ------------------------------------------------------------------ the router page's 3f parts

type driftBand struct {
	Text   string
	Repair bool // auto-repair off: Repair is offered
}

type awaitingBand struct {
	Text, Error, Key string
}

type removingBand struct {
	JobHref string
	Failed  bool
	Error   string
}

type unmanagedRow struct {
	Tag, Entries, Since string
	Hint                string
	Adoptable           bool
	AdoptHref, BaseHref string
	RemoveTitle         string
}

type hopRow struct {
	Label, Result, Kind, Detail string
}

// lifeOf fills the router view's bands and areas of 3f.
func (h *handler) lifeOf(ctx context.Context, v *routerView) error {
	r := v.R
	loc := i18n.From(ctx)
	switch r.State {
	case routers.StateAwaiting:
		b := &awaitingBand{Text: i18n.T(ctx, "routers.band.awaiting", i18n.Args{"date": loc.ShortDate(r.AwaitingUntil)})}
		if !r.AwaitingUntil.After(h.Now()) {
			b.Text = i18n.T(ctx, "routers.band.awaiting_over", i18n.Args{"date": loc.ShortDate(r.AwaitingUntil)})
		}
		if key, _, err := h.SSH.PublicKey(ctx); err == nil {
			b.Key = key
		}
		probes, err := h.Jobs.List(ctx, jobs.Filter{Type: routers.JobProbe, Subject: routers.Subject(r.ID), Limit: 1})
		if err != nil {
			return err
		}
		if len(probes) > 0 && probes[0].State == jobs.Failed {
			b.Error = i18n.T(ctx, "routers.band.probe_error", i18n.Args{"time": loc.Time(probes[0].CreatedAt.Time), "error": probes[0].Error})
		}
		v.Awaiting = b
	case routers.StatePaused:
		v.Paused = true
	case routers.StateRemoving:
		b := &removingBand{}
		id, err := h.Routers.RemovalJob(ctx, r.ID)
		if err != nil {
			return err
		}
		if id != 0 {
			b.JobHref = "/jobs/" + i64(id)
			if j, err := h.Jobs.Job(ctx, id); err == nil && (j.State == jobs.Failed || j.State == jobs.Cancelled) {
				b.Failed, b.Error = true, r.LastError
			}
		} else {
			b.Failed = true
		}
		v.Removing = b
	}
	if len(r.Drift) > 0 {
		repair, err := h.Settings.GetBool(ctx, conf.DriftRepair)
		if err != nil {
			return err
		}
		d, _, err := h.Routers.LatestDrift(ctx, r.ID)
		if err != nil {
			return err
		}
		var what []string
		for _, p := range d.Plan {
			switch n := int64(p.Want - p.Have); {
			case n > 0:
				what = append(what, loc.N("routers.drift.fewer", n, i18n.Args{"tag": p.Tag}))
			case n < 0:
				what = append(what, loc.N("routers.drift.more", -n, i18n.Args{"tag": p.Tag}))
			default:
				what = append(what, i18n.T(ctx, "routers.drift.differs", i18n.Args{"tag": p.Tag}))
			}
		}
		if len(what) == 0 {
			what = r.Drift
		}
		v.Drift = &driftBand{Repair: !repair, Text: i18n.T(ctx, "routers.band.drift", i18n.Args{
			"time": loc.Time(r.DriftCheckedAt), "what": strings.Join(what, "; "),
		})}
	}
	if v.Band != nil {
		if f, ok, err := h.Routers.LastFailure(ctx, r.ID); err != nil {
			return err
		} else if ok && f.Step == routers.StepConnect {
			v.Band.Hops = hopRows(ctx, routers.Hops(r, f, h.Routers.TailnetStatus(ctx)))
		}
	}
	us, err := h.Routers.Unmanaged(ctx, r.ID)
	if err != nil {
		return err
	}
	for _, u := range us {
		row := unmanagedRow{
			Tag: u.Tag, Entries: loc.N("routers.unmanaged.entries", int64(u.Entries)),
			Since: i18n.T(ctx, "routers.unmanaged.since", i18n.Args{"date": loc.ShortDate(u.FirstSeen)}), Adoptable: routers.Adoptable(u.Tag),
			BaseHref: unmanagedHref(r.ID, u.Tag), AdoptHref: unmanagedHref(r.ID, u.Tag) + "/adopt",
			RemoveTitle: loc.N("routers.unmanaged.remove.title", int64(u.Entries), i18n.Args{"tag": u.Tag, "router": r.Name}),
		}
		switch {
		case !row.Adoptable:
			row.Hint = i18n.T(ctx, "routers.unmanaged.invalid")
		case len(u.Catalog) > 0:
			row.Hint = i18n.T(ctx, "routers.unmanaged.catalog", i18n.Args{"selector": u.Catalog[0]})
		case u.Existing != nil:
			row.Hint = i18n.T(ctx, "routers.unmanaged.existing", i18n.Args{"tag": u.Tag})
		}
		if u.Ignored {
			v.Ignored = append(v.Ignored, row)
		} else {
			v.Unmanaged = append(v.Unmanaged, row)
		}
	}
	if r.ReadAt.IsZero() {
		return nil
	}
	if r.Untagged > 0 {
		v.Untagged = loc.N("routers.untagged", int64(r.Untagged))
	}
	if r.InfraPins > 0 {
		v.InfraPins = loc.N("routers.infra", int64(r.InfraPins))
	}
	every, err := h.Settings.GetDuration(ctx, conf.DriftEvery)
	if err != nil {
		return err
	}
	repair, err := h.Settings.GetBool(ctx, conf.DriftRepair)
	if err != nil {
		return err
	}
	key := "routers.conn.drift.off"
	if repair {
		key = "routers.conn.drift.on"
	}
	v.Conn.Drift = i18n.T(ctx, key, i18n.Args{"every": loc.Duration(every)})
	return nil
}

// hopRows words the per-hop table.
func hopRows(ctx context.Context, hops []routers.Hop) []hopRow {
	out := make([]hopRow, 0, len(hops))
	for _, hp := range hops {
		row := hopRow{Label: i18n.T(ctx, "routers.hop."+hp.Kind, i18n.Args{"name": hp.Label}), Detail: hp.Detail}
		switch hp.Result {
		case routers.HopUp:
			row.Kind, row.Result = "ok", i18n.T(ctx, "routers.hop.up")
			if hp.Kind == "tailnet" {
				row.Result = i18n.T(ctx, "routers.hop.up_tailnet")
			}
		case routers.HopNotTried:
			row.Kind, row.Result = "off", i18n.T(ctx, "routers.hop.not-tried")
			if hp.Via != "" {
				row.Result = i18n.T(ctx, "routers.hop.via", i18n.Args{"via": hp.Via})
			}
		default:
			row.Kind, row.Result = "broken", i18n.T(ctx, "routers.hop."+hp.Result)
		}
		out = append(out, row)
	}
	return out
}

// watchOf is the routers list's Watch cell: drift, then unmanaged tags.
func watchOf(ctx context.Context, h *handler, r routers.Router) (string, bool, error) {
	if len(r.Drift) > 0 {
		return i18n.T(ctx, "routers.watch.drift", i18n.Args{"tags": strings.Join(r.Drift, ", ")}), true, nil
	}
	us, err := h.Routers.Unmanaged(ctx, r.ID)
	if err != nil {
		return "", false, err
	}
	var tags []string
	for _, u := range us {
		if !u.Ignored {
			tags = append(tags, u.Tag)
		}
	}
	if len(tags) > 0 {
		return i18n.From(ctx).N("routers.watch.unmanaged", int64(len(tags)), i18n.Args{"tags": strings.Join(tags, ", ")}), true, nil
	}
	return i18n.T(ctx, "routers.watch.none"), false, nil
}

// sortRouters puts failing routers first, then the ones to watch, then by name.
func sortRouters(rows []routerRow) {
	rank := func(r routerRow) int {
		switch {
		case r.Failing:
			return 0
		case r.Watched:
			return 1
		}
		return 2
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := rank(rows[i]), rank(rows[j]); a != b {
			return a < b
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
}

// ------------------------------------------------------------------ actions

func (h *handler) routerRepair(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	if _, err := h.Routers.Repair(c.Request().Context(), r.ID, events.ActorAdmin); errors.Is(err, routers.ErrNotActive) {
		return echo.NewHTTPError(http.StatusConflict, "the router is not active")
	} else if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID)+"?repairing=1")
}

func (h *handler) routerPause(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	if err := h.Routers.Pause(c.Request().Context(), r.ID, events.ActorAdmin); errors.Is(err, routers.ErrState) {
		return echo.NewHTTPError(http.StatusConflict, "only an active router can be paused")
	} else if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID))
}

func (h *handler) routerResume(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	if err := h.Routers.Resume(c.Request().Context(), r.ID, events.ActorAdmin); errors.Is(err, routers.ErrState) {
		return echo.NewHTTPError(http.StatusConflict, "only a paused router can be resumed")
	} else if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID)+"?synced=1")
}

func (h *handler) loadUnmanaged(c *echo.Context) (routers.Router, routers.UnmanagedTag, error) {
	r, err := h.loadRouter(c)
	if err != nil {
		return r, routers.UnmanagedTag{}, err
	}
	u, err := h.Routers.UnmanagedOne(c.Request().Context(), r.ID, tagParam(c))
	if errors.Is(err, routers.ErrNoSuchTag) {
		return r, u, echo.ErrNotFound
	}
	return r, u, err
}

func (h *handler) unmanagedRemove(c *echo.Context) error {
	r, u, err := h.loadUnmanaged(c)
	if err != nil {
		return err
	}
	if _, err := h.Routers.RemoveUnmanaged(c.Request().Context(), r.ID, u.Tag, events.ActorAdmin); errors.Is(err, routers.ErrNotActive) {
		return echo.NewHTTPError(http.StatusConflict, "the router is not active")
	} else if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID)+"?synced=1")
}

func (h *handler) unmanagedIgnore(ignore bool) echo.HandlerFunc {
	return func(c *echo.Context) error {
		r, u, err := h.loadUnmanaged(c)
		if err != nil {
			return err
		}
		if err := h.Routers.Ignore(c.Request().Context(), r.ID, u.Tag, ignore, events.ActorAdmin); err != nil {
			return err
		}
		return web.Redirect(c, routerHref(r.ID)+"#unmanaged")
	}
}

// ------------------------------------------------------------------ adopt

type adoptOffer struct {
	How, Label, What string
}

type adoptView struct {
	R      routers.Router
	U      routers.UnmanagedTag
	Intro  string
	Offers []adoptOffer
	Error  string
	Base   string
}

func (h *handler) adoptViewOf(ctx context.Context, r routers.Router, u routers.UnmanagedTag) adoptView {
	loc := i18n.From(ctx)
	v := adoptView{R: r, U: u, Base: unmanagedHref(r.ID, u.Tag),
		Intro: loc.N("routers.adopt.intro", int64(u.Entries), i18n.Args{"tag": u.Tag, "router": r.Name, "list": r.List})}
	if !routers.Adoptable(u.Tag) {
		v.Intro = i18n.T(ctx, "routers.adopt.invalid", i18n.Args{"tag": u.Tag})
		return v
	}
	for _, sel := range u.Catalog {
		how, _, _ := strings.Cut(sel, ":")
		v.Offers = append(v.Offers, adoptOffer{How: how, Label: i18n.T(ctx, "routers.adopt.as", i18n.Args{"selector": sel}),
			What: i18n.T(ctx, "routers.adopt.as.what", i18n.Args{"list": r.List})})
	}
	if u.Existing != nil {
		v.Offers = append(v.Offers, adoptOffer{How: routers.AdoptExisting, Label: i18n.T(ctx, "routers.adopt.existing", i18n.Args{"tag": u.Tag, "list": r.List}),
			What: i18n.T(ctx, "routers.adopt.existing.what")})
	} else {
		v.Offers = append(v.Offers, adoptOffer{How: routers.AdoptCustom, Label: loc.N("routers.adopt.custom", int64(u.Entries)),
			What: i18n.T(ctx, "routers.adopt.custom.what", i18n.Args{"tag": u.Tag})})
	}
	return v
}

func (h *handler) adoptPage(c *echo.Context) error {
	r, u, err := h.loadUnmanaged(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	return web.Render(c, http.StatusOK, adoptPage(h.shell(c, i18n.T(ctx, "routers.adopt.title", i18n.Args{"tag": u.Tag}), routersPath), h.adoptViewOf(ctx, r, u)))
}

func (h *handler) adoptPost(c *echo.Context) error {
	r, u, err := h.loadUnmanaged(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	how := c.FormValue("how")
	_, err = h.Routers.Adopt(ctx, r.ID, u.Tag, how, events.ActorAdmin)
	var ge *lists.GuardError
	var taken *services.TagTakenError
	switch {
	case err == nil:
		return web.Redirect(c, routerHref(r.ID)+"?adopted="+url.QueryEscape(u.Tag))
	case errors.As(err, &ge):
		v := h.adoptViewOf(ctx, r, u)
		v.Error = ge.Error()
		return web.Render(c, http.StatusConflict, adoptPage(h.shell(c, i18n.T(ctx, "routers.adopt.title", i18n.Args{"tag": u.Tag}), routersPath), v))
	case errors.Is(err, routers.ErrInvalidTag), errors.Is(err, routers.ErrNoOffer), errors.As(err, &taken):
		return echo.NewHTTPError(http.StatusConflict, "that way of adopting isn't offered")
	}
	return err
}

// ------------------------------------------------------------------ remove

type rtRemoveView struct {
	R       routers.Router
	Applied int
}

func (h *handler) routerRemovePage(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	a, err := h.Routers.Applied(ctx, r.ID)
	if err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, routerRemovePage(h.shell(c, i18n.T(ctx, "routers.remove.title", i18n.Args{"name": r.Name}), routersPath),
		rtRemoveView{R: r, Applied: len(a)}))
}

func (h *handler) routerRemovePost(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	clean := c.FormValue("clean") == "1"
	if _, err := h.Routers.Remove(c.Request().Context(), r.ID, clean, events.ActorAdmin); errors.Is(err, routers.ErrState) {
		return echo.NewHTTPError(http.StatusConflict, "the router is being removed")
	} else if err != nil {
		return err
	}
	if clean {
		return web.Redirect(c, routerHref(r.ID))
	}
	return web.Redirect(c, routersPath+"?removed="+url.QueryEscape(r.Name))
}

// routerRemoveKeep removes a removing router without cleaning.
func (h *handler) routerRemoveKeep(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	if _, err := h.Routers.Remove(c.Request().Context(), r.ID, false, events.ActorAdmin); err != nil {
		return err
	}
	return web.Redirect(c, routersPath+"?removed="+url.QueryEscape(r.Name))
}

func (h *handler) routerRemoveRetry(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if r.State != routers.StateRemoving {
		return echo.NewHTTPError(http.StatusConflict, "the router is not being removed")
	}
	id, err := h.Routers.RemovalJob(ctx, r.ID)
	if err != nil {
		return err
	}
	if id == 0 {
		return echo.NewHTTPError(http.StatusConflict, "no removal to retry")
	}
	if _, err := h.Jobs.Retry(ctx, id, events.ActorAdmin); err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID))
}

// ------------------------------------------------------------------ dashboard

type dashRouter struct {
	ID               int64
	Name, Kind, Word string
}

type dashSnapshot struct {
	ID       int64
	Tag, Pct string
}

// DashView is the dashboard's Routing area.
type DashView struct {
	InSync    string
	Routers   []dashRouter
	Snapshots []dashSnapshot
	Digest    string
}

// Dashboard is the Routing area: routers to look at first (failing, drift,
// unmanaged tags), else every router with its last sync; the snapshots
// waiting for a decision; the last daily refresh. Nil with no routers and
// no services.
func Dashboard(ctx context.Context, d Deps) (*DashView, error) {
	h := &handler{Deps: d}
	loc := i18n.From(ctx)
	rs, err := d.Routers.List(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.Services.List(ctx, services.Filter{State: "waiting"})
	if err != nil {
		return nil, err
	}
	ev, digested, err := d.Refresh.LastDigest(ctx)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 && len(rows) == 0 && !digested {
		return nil, nil
	}
	v := &DashView{}
	var attention, rest []dashRouter
	inSync := 0
	for _, r := range rs {
		row := dashRouter{ID: r.ID, Name: r.Name}
		watch, watched, err := watchOf(ctx, h, r)
		if err != nil {
			return nil, err
		}
		switch {
		case r.State == routers.StateActive && r.LastResult == "failed":
			step := ""
			if f, ok, err := d.Routers.LastFailure(ctx, r.ID); err != nil {
				return nil, err
			} else if ok {
				step = f.Step
			}
			row.Kind, row.Word = "broken", i18n.T(ctx, "routers.dash.failed", i18n.Args{"step": step, "ago": loc.Ago(r.LastSyncAt)})
			attention = append(attention, row)
			continue
		case watched:
			row.Kind, row.Word = "look", strings.TrimPrefix(watch, "▲ ")
			attention = append(attention, row)
			continue
		}
		switch {
		case r.State == routers.StateAwaiting:
			row.Kind, row.Word = "look", i18n.T(ctx, "routers.state.awaiting")
		case r.State != routers.StateActive:
			row.Kind, row.Word = "off", i18n.T(ctx, "routers.state."+r.State)
		case r.LastResult == "synced":
			inSync++
			row.Kind, row.Word = "ok", i18n.T(ctx, "routers.word.synced", i18n.Args{"ago": loc.Ago(r.LastSyncAt)})
		default:
			row.Kind, row.Word = "unknown", i18n.T(ctx, "routers.word.never")
		}
		rest = append(rest, row)
	}
	if len(attention) > 0 {
		// awaiting routers stay listed: they need the router script run
		v.Routers = attention
		for _, r := range rest {
			if r.Kind == "look" {
				v.Routers = append(v.Routers, r)
			}
		}
	} else {
		v.Routers = rest
	}
	if len(rs) > 0 {
		v.InSync = loc.N("routers.dash.in_sync", int64(inSync))
	}
	for _, s := range rows {
		sn, ok, err := d.Refresh.Waiting(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		v.Snapshots = append(v.Snapshots, dashSnapshot{ID: s.ID, Tag: s.Tag, Pct: i18n.T(ctx, "routers.dash.drop", i18n.Args{"pct": int64(sn.LostPct)})})
	}
	v.Digest = i18n.T(ctx, "routers.dash.refresh.none")
	if digested {
		p := ev.Payload
		v.Digest = loc.N("routers.dash.digest", int64(num(p["changed"])), i18n.Args{
			"time": loc.Time(ev.Time.Time), "added": loc.Number(int64(num(p["added"]))), "removed": loc.Number(int64(num(p["removed"]))),
			"rejected": loc.Number(int64(num(p["rejected"]))),
		})
	}
	return v, nil
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

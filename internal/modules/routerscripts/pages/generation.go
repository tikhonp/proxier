package pages

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// loadGeneration reads the generation of the :gid parameter; 404 when there
// is none.
func (h *handler) loadGeneration(c *echo.Context) (generations.Generation, error) {
	id, err := strconv.ParseInt(c.Param("gid"), 10, 64)
	if err != nil {
		return generations.Generation{}, echo.ErrNotFound
	}
	g, err := h.Generations.Get(c.Request().Context(), id)
	if errors.Is(err, generations.ErrNotFound) {
		return g, echo.ErrNotFound
	}
	return g, err
}

type valueRow struct {
	Name, Value, Note string
	Secret, Changed   bool
}

type generationView struct {
	G           generations.Generation
	Line        string
	Changes     string // "2 lines differ from v4: lanNet, subUrl"
	Newer       string // "v5 is current now"
	RouterState string
	LinkState   string
	Changed     []valueRow
	All         []valueRow
	CanGenerate bool
	HasRouters  bool
	HasLinks    bool
	Activity    []eventLine
	Fetch       fetchView
	After       afterView
	History     []historyRow
}

func (h *handler) generationPage(c *echo.Context) error {
	g, err := h.loadGeneration(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	v := generationView{G: g, HasRouters: h.Generations.HasRouters(), HasLinks: h.Generations.HasLinks()}
	v.Line = i18n.T(ctx, "generations.page.line", i18n.Args{"script": g.Script, "version": g.Version, "time": loc.Time(g.CreatedAt), "by": g.CreatedBy})
	v.Changes = changesText(ctx, g.Version, g.Changed)
	if g.Current != g.Version {
		v.Newer = i18n.T(ctx, "generations.page.newer", i18n.Args{"version": g.Current})
	}
	if g.Router != nil {
		v.RouterState = i18n.T(ctx, "generations.router_state."+g.Router.State)
	}
	if g.Link != nil {
		v.LinkState = i18n.T(ctx, "generations.link_state."+g.Link.State)
	}
	for _, val := range g.Values {
		r := valueRow{Name: val.Name, Value: val.Value, Secret: val.Secret, Changed: val.Changed}
		if val.Source != "" {
			r.Note = i18n.T(ctx, "generations.source."+val.Source)
		}
		if val.Changed {
			v.Changed = append(v.Changed, r)
		}
		v.All = append(v.All, r)
	}
	if sc, err := h.Scripts.Get(ctx, g.ScriptID); err == nil {
		v.CanGenerate = !sc.Archived && sc.Current > 0
	}
	if v.Activity, err = h.generationActivity(ctx, g); err != nil {
		return err
	}
	fv, list, err := h.fetchState(ctx, g, false)
	if err != nil {
		return err
	}
	v.Fetch, v.After, v.History = fv, h.after(ctx, g, fv, list), history(ctx, list)
	c.Response().Header().Set("Cache-Control", "no-store")
	title := g.RouterName
	return web.Render(c, http.StatusOK, generationPage(h.shell(c, title, ListPath), v))
}

// changesText is "2 lines differ from v4: lanNet, subUrl".
func changesText(ctx context.Context, version int, changed []string) string {
	if len(changed) == 0 {
		return i18n.T(ctx, "generations.changes.none", i18n.Args{"version": version})
	}
	return i18n.N(ctx, "generations.changes", int64(len(changed)), i18n.Args{"version": version, "names": strings.Join(changed, ", ")})
}

// generationActivity is the last 10 events of a generation, as sentences.
func (h *handler) generationActivity(ctx context.Context, g generations.Generation) ([]eventLine, error) {
	loc := i18n.From(ctx)
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: generations.Subject(g.ID), Limit: 10})
	if err != nil {
		return nil, err
	}
	out := make([]eventLine, 0, len(list))
	for _, e := range list {
		args := i18n.Args{"subject": g.RouterName, "actor": e.Actor}
		for k, v := range e.Payload {
			args[k] = v
		}
		text := e.Type
		if key := "event." + e.Type; loc.Has(key) {
			text = loc.T(key, i18n.Args(plainArgs(args)))
		}
		out = append(out, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return out, nil
}

// generationDownload is the filled file: a session only, and it records
// nothing (the request log has it).
func (h *handler) generationDownload(c *echo.Context) error {
	g, err := h.loadGeneration(c)
	if err != nil {
		return err
	}
	body, err := h.Generations.Body(c.Request().Context(), g.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+g.FileName+`"`)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", body)
}

type changesView struct {
	G       generations.Generation
	Title   string
	Split   bool
	Same    bool
	Diff    ui.DiffView
	Summary string
}

// generationChanges is the version against the file, secrets masked.
func (h *handler) generationChanges(c *echo.Context) error {
	g, err := h.loadGeneration(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	unified, err := h.Generations.Changes(ctx, g.ID)
	if err != nil {
		return err
	}
	v := changesView{G: g, Split: c.QueryParam("view") == "split", Same: unified == "", Summary: changesText(ctx, g.Version, g.Changed)}
	v.Title = i18n.T(ctx, "generations.changes.title", i18n.Args{"file": g.FileName})
	if !v.Same {
		v.Diff = ui.DiffView{Split: v.Split, Files: []ui.DiffFile{{Path: g.FileName, Change: "changed", Hunks: ui.ParseUnified(unified)}}}
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, changesPage(h.shell(c, v.Title, ListPath), v))
}

// fetchView is the Fetch URL area: the waiting URL, masked unless revealed,
// with the two commands; Copy buttons always carry the real text.
type fetchView struct {
	GID           int64
	Live          bool
	Revealed      bool
	URL, Masked   string
	Fetch, Import string // with the real URL
	MFetch        string // with the masked URL
	Both          string
	State         string // the state line
	StateClass    string
	Expired       bool
}

// afterView is the After the import area.
type afterView struct {
	GID         int64
	Poll        bool
	Fetch       string // step 1
	FetchClass  string
	FetchNote   string
	KeyParam    string // step 2: the @fill proxier-ssh-key parameter
	KeyCommands string // without one, with a router
	Router      string // step 3
	RouterClass string
	RouterHref  string // "": no link
	State       fetchView
}

type historyRow struct {
	Created, Expires string
	State, Class     string
}

// hm is a time of day in the display zone: "17:30".
func hm(loc *i18n.Localizer, t time.Time) string { return t.In(loc.TZ).Format("15:04") }

// fetchState reads the generation's fetch URLs: the area, and the list
// newest first.
func (h *handler) fetchState(ctx context.Context, g generations.Generation, revealed bool) (fetchView, []generations.FetchURL, error) {
	loc := i18n.From(ctx)
	v := fetchView{GID: g.ID, Revealed: revealed, Masked: h.Generations.MaskedURL()}
	u, link, live, err := h.Generations.Live(ctx, g.ID)
	if err != nil {
		return v, nil, err
	}
	list, err := h.Generations.FetchURLs(ctx, g.ID)
	if err != nil {
		return v, nil, err
	}
	now := h.Now()
	switch {
	case live:
		v.Live, v.URL = true, link
		v.Fetch, v.Import = generations.Commands(link, g.Slug)
		v.MFetch, _ = generations.Commands(v.Masked, g.Slug)
		v.Both = v.Fetch + "\n" + v.Import
		mins := int64((u.ExpiresAt.Sub(now) + time.Minute - 1) / time.Minute)
		v.State = i18n.N(ctx, "generations.fetch.expires", mins, i18n.Args{"time": hm(loc, u.ExpiresAt)})
	case len(list) == 0:
		v.State, v.StateClass = i18n.T(ctx, "generations.fetch.none"), "subtle"
	case list[0].State == generations.FetchUsed:
		v.State, v.StateClass = i18n.T(ctx, "generations.fetch.used", i18n.Args{"time": hm(loc, list[0].EndedAt)}), "foam"
	default:
		v.State, v.StateClass, v.Expired = i18n.T(ctx, "generations.fetch.expired"), "gold", true
	}
	return v, list, nil
}

// after builds the After the import area.
func (h *handler) after(ctx context.Context, g generations.Generation, state fetchView, list []generations.FetchURL) afterView {
	loc := i18n.From(ctx)
	now := h.Now()
	a := afterView{GID: g.ID, State: state}
	var used, ended *generations.FetchURL
	for i := range list {
		if list[i].State == generations.FetchUsed && used == nil {
			used = &list[i]
		}
		if ended == nil && list[i].State != generations.FetchReplaced {
			ended = &list[i]
		}
	}
	switch {
	case state.Live:
		a.Fetch, a.FetchClass, a.FetchNote = i18n.T(ctx, "generations.after.fetch.waiting"), "gold", i18n.T(ctx, "generations.after.fetch.waiting_note")
		a.Poll = true
	case used != nil:
		a.Fetch, a.FetchClass = i18n.T(ctx, "generations.after.fetch.done", i18n.Args{"time": hm(loc, used.EndedAt), "ip": used.IP}), "foam"
		if used.UserAgent != "" {
			a.Fetch += " · " + used.UserAgent
		}
	case ended != nil:
		a.Fetch, a.FetchClass = i18n.T(ctx, "generations.after.fetch.expired"), "subtle"
	default:
		a.Fetch, a.FetchClass = i18n.T(ctx, "generations.after.fetch.none"), "subtle"
	}
	for _, val := range g.Values {
		if val.Source == "key" {
			a.KeyParam = val.Name
		}
	}
	if a.KeyParam == "" && g.Router != nil {
		a.KeyCommands = g.Router.KeyCommands
	}
	r := g.Router
	switch {
	case r == nil && g.RouterGone:
		a.Router, a.RouterClass = i18n.T(ctx, "generations.after.router.removed"), "subtle"
	case r == nil:
		a.Router, a.RouterClass = i18n.T(ctx, "generations.after.router.none"), "subtle"
	default:
		a.RouterHref = "/routing/routers/" + i64(r.ID)
		switch r.State {
		case "awaiting":
			if now.Before(r.AwaitingUntil) {
				key, args := "generations.after.router.awaiting", i18n.Args{"name": r.Name, "date": loc.ShortDate(r.AwaitingUntil)}
				if r.Jump != "" {
					key, args["jump"] = "generations.after.router.awaiting_jump", jumpName(r.Jump)
				}
				a.Router, a.RouterClass, a.Poll = i18n.T(ctx, key, args), "gold", true
			} else {
				a.Router, a.RouterClass = i18n.T(ctx, "generations.after.router.never"), "love"
			}
		case "active":
			conn := i18n.T(ctx, "generations.after.router.connected", i18n.Args{"time": hm(loc, r.ConnectedAt)})
			switch r.LastResult {
			case "synced":
				a.Router, a.RouterClass = conn+" · "+i18n.T(ctx, "generations.after.router.synced", i18n.Args{"time": hm(loc, r.LastSyncAt)}), "foam"
			case "failed":
				a.Router, a.RouterClass = conn+" · "+i18n.T(ctx, "generations.after.router.failed", i18n.Args{"error": r.LastError}), "love"
			default:
				a.Router, a.RouterClass, a.Poll = conn+" · "+i18n.T(ctx, "generations.after.router.queued"), "gold", true
			}
		default:
			a.Router, a.RouterClass = i18n.T(ctx, "generations.router_state."+r.State), "subtle"
		}
	}
	return a
}

// jumpName is a jump host without the usual port: "dacha-pi".
func jumpName(addr string) string {
	if host, port, err := net.SplitHostPort(addr); err == nil && port == "22" {
		return host
	}
	return addr
}

// history is the Fetch history rows.
func history(ctx context.Context, list []generations.FetchURL) []historyRow {
	loc := i18n.From(ctx)
	out := make([]historyRow, 0, len(list))
	for _, u := range list {
		r := historyRow{
			Created: i18n.T(ctx, "generations.history.created", i18n.Args{"time": loc.Time(u.CreatedAt), "by": u.CreatedBy}),
			Expires: i18n.T(ctx, "generations.history.expires", i18n.Args{"time": loc.Time(u.ExpiresAt)}),
		}
		switch u.State {
		case generations.FetchUsed:
			r.State, r.Class = i18n.T(ctx, "generations.history.used", i18n.Args{"time": loc.Time(u.EndedAt), "ip": u.IP}), "foam"
			if u.UserAgent != "" {
				r.State += " · " + u.UserAgent
			}
		case generations.FetchExpired:
			r.State, r.Class = i18n.T(ctx, "generations.history.expired", i18n.Args{"time": loc.Time(u.EndedAt)}), "subtle"
		case generations.FetchReplaced:
			r.State, r.Class = i18n.T(ctx, "generations.history.replaced", i18n.Args{"time": loc.Time(u.EndedAt)}), "subtle"
		default:
			r.State, r.Class = i18n.T(ctx, "generations.history.waiting"), "gold"
		}
		out = append(out, r)
	}
	return out
}

// fetchURLCreate creates (or replaces) the generation's fetch URL.
func (h *handler) fetchURLCreate(c *echo.Context) error {
	g, err := h.loadGeneration(c)
	if err != nil {
		return err
	}
	if _, _, err := h.Generations.CreateFetchURL(c.Request().Context(), g.ID, events.ActorAdmin); err != nil {
		return err
	}
	return web.Redirect(c, GenerationHref(g.ID)+"#fetch-url")
}

// fetchURLReveal is the URL and commands in clear, or masked with ?hide=1.
// Without a live URL the area as it is now.
func (h *handler) fetchURLReveal(c *echo.Context) error {
	g, err := h.loadGeneration(c)
	if err != nil {
		return err
	}
	v, _, err := h.fetchState(c.Request().Context(), g, c.QueryParam("hide") != "1")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, fetchURLBox(v))
}

// afterArea is the After the import area for polling, with the Fetch URL
// area's state line out of band.
func (h *handler) afterArea(c *echo.Context) error {
	g, err := h.loadGeneration(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v, list, err := h.fetchState(ctx, g, false)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, afterFragment(h.after(ctx, g, v, list), true))
}

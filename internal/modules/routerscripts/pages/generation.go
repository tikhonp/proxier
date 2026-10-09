package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

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

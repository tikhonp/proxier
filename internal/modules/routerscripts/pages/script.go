package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ------------------------------------------------------------------ the panel

type panelItem struct {
	Name, Default, Ann, Desc string
	Computed                 bool
	Expr, Value              string
}

type panelGroup struct {
	Heading string
	Items   []panelItem
}

type panelProblem struct {
	Line      int
	Sev, Text string
}

// panelView is the detected parameters: the draft's (Live, following the
// text) or a version's.
type panelView struct {
	Live     bool
	ScriptID int64
	Block    bool
	Params   int
	Computed int
	Groups   []panelGroup
	Problems []panelProblem
}

func makePanel(ctx context.Context, p params.Script, fs []params.Finding) panelView {
	v := panelView{Block: p.Block, Params: len(p.Params), Computed: len(p.Computed)}
	vals := params.Eval(p, nil)
	for _, g := range p.Groups {
		pg := panelGroup{Heading: g.Heading}
		for i, it := range g.Items {
			desc := it.Description()
			if i == 0 && g.Heading != "" {
				desc = ""
			}
			if pp := it.Param; pp != nil {
				def := pp.Literal
				if pp.Annotations.Secret {
					def = "•••"
				}
				pg.Items = append(pg.Items, panelItem{Name: pp.Name, Default: def, Ann: annotationsText(pp.Annotations), Desc: desc})
				continue
			}
			c := it.Computed
			pg.Items = append(pg.Items, panelItem{Name: c.Name, Desc: desc, Computed: true, Expr: c.Expr, Value: vals[c.Name]})
		}
		v.Groups = append(v.Groups, pg)
	}
	for _, f := range fs {
		v.Problems = append(v.Problems, panelProblem{Line: f.Line, Sev: f.Severity, Text: findingText(ctx, f)})
	}
	return v
}

// panel answers the panel for the posted body; it records nothing.
func (h *handler) panel(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	basedOn := 0
	if d, err := h.Scripts.Draft(ctx, s.ID); err == nil {
		basedOn = d.BasedOn
	} else if !errors.Is(err, scripts.ErrNoDraft) {
		return err
	}
	p, fs, err := h.Scripts.ReportFor(ctx, s.ID, c.FormValue("body"), basedOn)
	if err != nil {
		return err
	}
	v := makePanel(ctx, p, fs)
	v.Live, v.ScriptID = true, s.ID
	return web.Render(c, http.StatusOK, paramsPanel(v))
}

// ------------------------------------------------------------------ the script page

type versionRow struct {
	Number                 int
	Published, Notes, Gens string
	Current, HasPrev       bool
}

type scriptView struct {
	S              scripts.Script
	Line           string
	Band, BandKind string
	Stale          bool
	Draft          bool
	BasedOn        int
	Body           string
	Revision       int
	BodyErr        string
	Next           int
	Panel          panelView
	Versions       []versionRow
	Generations    int
	Activity       []eventLine
}

func (h *handler) page(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := scriptView{S: s}
	switch q := c.QueryParam; {
	case q("created") == "1":
		v.Band, v.BandKind = i18n.T(ctx, "scripts.band.created"), "pine"
	case q("saved") == "1":
		v.Band, v.BandKind = i18n.T(ctx, "scripts.band.saved"), "pine"
	case q("published") != "":
		v.Band, v.BandKind = i18n.T(ctx, "scripts.band.published", i18n.Args{"version": q("published")}), "pine"
	case q("current") != "":
		v.Band, v.BandKind = i18n.T(ctx, "scripts.band.current", i18n.Args{"version": q("current")}), "pine"
	case q("discarded") == "1":
		v.Band, v.BandKind = i18n.T(ctx, "scripts.band.discarded"), "pine"
	}
	d, err := h.Scripts.Draft(ctx, s.ID)
	switch {
	case err == nil:
		v.Draft, v.BasedOn, v.Body, v.Revision = true, d.BasedOn, d.Body, d.Revision
	case !errors.Is(err, scripts.ErrNoDraft):
		return err
	}
	return h.renderPage(c, http.StatusOK, v)
}

// renderPage fills in everything but the draft's text and the bands.
func (h *handler) renderPage(c *echo.Context, code int, v scriptView) error {
	ctx := c.Request().Context()
	s := v.S
	vs, err := h.Scripts.Versions(ctx, s.ID)
	if err != nil {
		return err
	}
	v.Next = 1
	if len(vs) > 0 {
		v.Next = vs[0].Number + 1
	}
	loc := i18n.From(ctx)
	for _, ver := range vs {
		row := versionRow{Number: ver.Number, Published: i18n.T(ctx, "scripts.version.published", i18n.Args{"day": loc.Date(ver.PublishedAt), "by": ver.PublishedBy}),
			Notes: ver.Notes, Gens: loc.N("scripts.version.gens", int64(ver.Generations)), Current: ver.Current, HasPrev: ver.Number > 1}
		v.Versions = append(v.Versions, row)
	}
	if v.Generations, err = h.Scripts.Generations(ctx, s.ID); err != nil {
		return err
	}
	if v.Draft {
		p, fs, err := h.Scripts.ReportFor(ctx, s.ID, v.Body, v.BasedOn)
		if err != nil {
			return err
		}
		v.Panel = makePanel(ctx, p, fs)
		v.Panel.Live, v.Panel.ScriptID = true, s.ID
	}
	v.Line = statusLine(ctx, s, v)
	if v.Activity, err = h.activity(ctx, s); err != nil {
		return err
	}
	sh := h.shell(c, s.Name, ListPath)
	if v.Draft {
		sh.Scripts = []string{"js/editor.bundle.js"}
	}
	return web.Render(c, code, scriptPage(sh, v))
}

// statusLine is "current v4 · draft based on v4 · 3 generations".
func statusLine(ctx context.Context, s scripts.Script, v scriptView) string {
	loc := i18n.From(ctx)
	out := i18n.T(ctx, "scripts.line.current", i18n.Args{"version": strconv.Itoa(s.Current)})
	if s.Current == 0 {
		out = i18n.T(ctx, "scripts.no_version")
	}
	switch {
	case v.Draft && v.BasedOn > 0:
		out += " · " + i18n.T(ctx, "scripts.line.draft_based", i18n.Args{"version": strconv.Itoa(v.BasedOn)})
	case v.Draft:
		out += " · " + i18n.T(ctx, "scripts.line.draft")
	}
	if s.Current > 0 {
		out += " · " + loc.N("scripts.line.gens", int64(v.Generations))
	}
	if s.Archived {
		out += " · " + i18n.T(ctx, "scripts.archived")
	}
	return out
}

// edit opens the draft, making one from the current version.
func (h *handler) edit(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	if _, err := h.Scripts.EditDraft(c.Request().Context(), s.ID, events.ActorAdmin); err != nil {
		return notFoundOr(err)
	}
	return web.Redirect(c, ScriptHref(s.ID)+"#draft")
}

func (h *handler) saveDraft(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	d, err := h.Scripts.Draft(ctx, s.ID)
	if errors.Is(err, scripts.ErrNoDraft) {
		return web.Redirect(c, ScriptHref(s.ID))
	}
	if err != nil {
		return err
	}
	body, _, err := formBody(c, "body", d.Body)
	if err != nil {
		return err
	}
	rev, _ := strconv.Atoi(c.FormValue("revision"))
	_, err = h.Scripts.SaveDraft(ctx, s.ID, rev, body, events.ActorAdmin)
	v := scriptView{S: s, Draft: true, BasedOn: d.BasedOn, Body: body, Revision: rev}
	var fe store.FieldErrors
	switch {
	case errors.Is(err, scripts.ErrDraftChanged):
		// the posted text stays in the editor; Save and Publish are off
		v.Stale = true
		return h.renderPage(c, http.StatusConflict, v)
	case errors.As(err, &fe):
		v.BodyErr = i18n.T(ctx, fe["body"])
		return h.renderPage(c, http.StatusUnprocessableEntity, v)
	case err != nil:
		return err
	}
	if c.FormValue("next") == "publish" {
		return web.Redirect(c, ScriptHref(s.ID)+"/publish")
	}
	return web.Redirect(c, ScriptHref(s.ID)+"?saved=1#draft")
}

func (h *handler) discard(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	err = h.Scripts.DiscardDraft(c.Request().Context(), s.ID, events.ActorAdmin)
	if errors.Is(err, scripts.ErrNoVersion) {
		return echo.NewHTTPError(http.StatusConflict, "the script has no version")
	}
	if err != nil && !errors.Is(err, scripts.ErrNoDraft) {
		return err
	}
	return web.Redirect(c, ScriptHref(s.ID)+"?discarded=1")
}

// ------------------------------------------------------------------ publish

type publishView struct {
	S                scripts.Script
	Next             int
	Revision         int
	Errors, Warnings []panelProblem
	Notes            string
	NotesErr         string
	Confirm          bool
	Unticked         bool // a post with warnings and no tick
	Stale            bool
	Generations      int
}

func problems(ctx context.Context, fs []params.Finding, sev string) []panelProblem {
	var out []panelProblem
	for _, f := range fs {
		if f.Severity == sev {
			out = append(out, panelProblem{Line: f.Line, Sev: sev, Text: findingText(ctx, f)})
		}
	}
	return out
}

func (h *handler) renderPublish(c *echo.Context, code int, v publishView) error {
	ctx := c.Request().Context()
	vs, err := h.Scripts.Versions(ctx, v.S.ID)
	if err != nil {
		return err
	}
	v.Next = 1
	if len(vs) > 0 {
		v.Next = vs[0].Number + 1
	}
	if v.Generations, err = h.Scripts.Generations(ctx, v.S.ID); err != nil {
		return err
	}
	if !v.Stale {
		_, fs, err := h.Scripts.Report(ctx, v.S.ID)
		if err != nil {
			return err
		}
		v.Errors, v.Warnings = problems(ctx, fs, params.Error), problems(ctx, fs, params.Warning)
	}
	title := i18n.T(ctx, "scripts.publish.title", i18n.Args{"version": strconv.Itoa(v.Next)})
	return web.Render(c, code, publishPage(h.shell(c, title, ListPath), v, title))
}

func (h *handler) publishPage(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	d, err := h.Scripts.Draft(c.Request().Context(), s.ID)
	if errors.Is(err, scripts.ErrNoDraft) {
		return web.Redirect(c, ScriptHref(s.ID))
	}
	if err != nil {
		return err
	}
	return h.renderPublish(c, http.StatusOK, publishView{S: s, Revision: d.Revision})
}

func (h *handler) publish(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rev, _ := strconv.Atoi(c.FormValue("revision"))
	v := publishView{S: s, Revision: rev, Notes: c.FormValue("notes"), Confirm: c.FormValue("confirm") == "1"}
	n, err := h.Scripts.Publish(ctx, s.ID, rev, v.Notes, v.Confirm, events.ActorAdmin)
	var (
		pe *scripts.PublishError
		fe store.FieldErrors
	)
	switch {
	case errors.Is(err, scripts.ErrNoDraft):
		return web.Redirect(c, ScriptHref(s.ID))
	case errors.Is(err, scripts.ErrDraftChanged):
		v.Stale = true
		return h.renderPublish(c, http.StatusConflict, v)
	case errors.As(err, &pe):
		v.Unticked = !pe.Blocked
		return h.renderPublish(c, http.StatusUnprocessableEntity, v)
	case errors.As(err, &fe):
		v.NotesErr = i18n.T(ctx, fe["notes"])
		return h.renderPublish(c, http.StatusUnprocessableEntity, v)
	case err != nil:
		return err
	}
	return web.Redirect(c, ScriptHref(s.ID)+"?published="+strconv.Itoa(n))
}

package pages

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) loadVersion(c *echo.Context, s scripts.Script) (scripts.Version, error) {
	n, err := strconv.Atoi(c.Param("n"))
	if err != nil || n < 1 {
		return scripts.Version{}, echo.ErrNotFound
	}
	v, err := h.Scripts.Version(c.Request().Context(), s.ID, n)
	return v, notFoundOr(err)
}

type versionView struct {
	S        scripts.Script
	V        scripts.Version
	Info     string
	Lines    []ui.CodeLine
	Panel    panelView
	Others   []int // versions to diff with
	DiffFrom int
}

func (h *handler) version(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ver, err := h.loadVersion(c, s)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	lines := ui.Highlight(s.Slug+".rsc", []byte(ver.Body))
	for _, f := range ver.Warnings {
		if f.Line >= 1 && f.Line <= len(lines) {
			lines[f.Line-1].Marks = append(lines[f.Line-1].Marks, ui.CodeMark{Severity: f.Severity, Text: findingText(ctx, f)})
		}
	}
	p := params.Parse([]byte(ver.Body))
	v := versionView{S: s, V: ver, Lines: lines, Panel: makePanel(ctx, p, nil), DiffFrom: ver.Number - 1}
	v.Info = i18n.T(ctx, "scripts.version.published", i18n.Args{"day": loc.Date(ver.PublishedAt), "by": ver.PublishedBy})
	if ver.Current {
		v.Info += " · " + i18n.T(ctx, "scripts.version.current")
	}
	// the confirmed warnings that have no line (no block, a gone parameter)
	for _, f := range ver.Warnings {
		if f.Line < 1 || f.Line > len(lines) {
			v.Panel.Problems = append(v.Panel.Problems, panelProblem{Line: f.Line, Sev: f.Severity, Text: findingText(ctx, f)})
		}
	}
	vs, err := h.Scripts.Versions(ctx, s.ID)
	if err != nil {
		return err
	}
	for _, o := range vs {
		if o.Number != ver.Number {
			v.Others = append(v.Others, o.Number)
		}
	}
	if v.DiffFrom == 0 && len(v.Others) > 0 {
		v.DiffFrom = v.Others[0]
	}
	title := s.Name + " v" + strconv.Itoa(ver.Number)
	return web.Render(c, http.StatusOK, versionPage(h.shell(c, title, ListPath), v, title))
}

// download is the version's body byte for byte. An admin route: a version
// holds whatever the admin pasted, defaults included.
func (h *handler) download(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ver, err := h.loadVersion(c, s)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+s.Slug+"-v"+strconv.Itoa(ver.Number)+`.rsc"`)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", []byte(ver.Body))
}

func (h *handler) makeCurrent(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ver, err := h.loadVersion(c, s)
	if err != nil {
		return err
	}
	if err := h.Scripts.MakeCurrent(c.Request().Context(), s.ID, ver.Number, events.ActorAdmin); err != nil {
		return notFoundOr(err)
	}
	return web.Redirect(c, ScriptHref(s.ID)+"?current="+strconv.Itoa(ver.Number))
}

// ------------------------------------------------------------------ diff

type diffOption struct{ Value, Label string }

type diffView struct {
	S        scripts.Script
	From, To string
	Split    bool
	Options  []diffOption
	Same     bool
	Diff     ui.DiffView
	Title    string
}

func (h *handler) diff(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	from, to := c.QueryParam("from"), c.QueryParam("to")
	d, derr := h.Scripts.Draft(ctx, s.ID)
	if derr != nil && !errors.Is(derr, scripts.ErrNoDraft) {
		return derr
	}
	if from == "" && to == "" {
		switch {
		case derr == nil && d.BasedOn > 0:
			from, to = strconv.Itoa(d.BasedOn), scripts.DraftRef
		case s.Current > 1:
			from, to = strconv.Itoa(s.Current-1), strconv.Itoa(s.Current)
		case s.Current == 1:
			from, to = "1", "1"
		default:
			from, to = scripts.DraftRef, scripts.DraftRef
		}
	}
	unified, err := h.Scripts.Diff(ctx, s.ID, from, to)
	if err != nil {
		return notFoundOr(err)
	}
	v := diffView{S: s, From: from, To: to, Split: c.QueryParam("view") == "split", Same: unified == ""}
	name := func(ref string) string {
		if ref == scripts.DraftRef {
			return i18n.T(ctx, "scripts.diff.draft")
		}
		return "v" + ref
	}
	v.Title = i18n.T(ctx, "scripts.diff.title", i18n.Args{"name": s.Name, "from": name(from), "to": name(to)})
	if derr == nil {
		v.Options = append(v.Options, diffOption{scripts.DraftRef, name(scripts.DraftRef)})
	}
	vs, err := h.Scripts.Versions(ctx, s.ID)
	if err != nil {
		return err
	}
	for _, o := range vs {
		v.Options = append(v.Options, diffOption{strconv.Itoa(o.Number), "v" + strconv.Itoa(o.Number)})
	}
	if !v.Same {
		v.Diff = ui.DiffView{Split: v.Split, Files: []ui.DiffFile{{Path: s.Slug + ".rsc", Change: "changed", Hunks: ui.ParseUnified(unified)}}}
	}
	return web.Render(c, http.StatusOK, diffPage(h.shell(c, v.Title, ListPath), v))
}

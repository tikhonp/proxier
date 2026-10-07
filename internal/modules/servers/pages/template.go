package pages

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ---- template page ----------------------------------------------------------

type draftSummary struct {
	templates.Info
	Files   int
	Changed int // files that differ from the version it is based on
}

type detailsForm struct{ Name, Slug, Description string }

type templateView struct {
	T        templates.Info
	Versions []templates.VersionInfo
	Draft    *draftSummary
	Saved    string
	Version  int               // the version just published, for Saved
	Error    string            // translated, for the whole page
	InUse    bool              // the error is "can't delete": offer Archive
	Errs     map[string]string // translated, by field of the details form
	Typed    *detailsForm
}

func (h *handler) renderTemplate(c *echo.Context, status int, id int64, v templateView) error {
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	v.T = info
	if v.Versions, err = h.Templates.Versions(ctx, id); err != nil {
		return err
	}
	if info.HasDraft {
		d, err := h.Templates.Draft(ctx, id)
		if err != nil && !errors.Is(err, templates.ErrNoDraft) {
			return err
		}
		if err == nil {
			ds := &draftSummary{Info: info, Files: len(d.Files)}
			if d.BasedOn > 0 {
				base, err := h.Templates.Version(ctx, id, d.BasedOn)
				if err == nil {
					for _, f := range templates.Diff(base.Files, d.Files) {
						if f.Change != templates.Unchanged {
							ds.Changed++
						}
					}
				}
			} else {
				ds.Changed = len(d.Files)
			}
			v.Draft = ds
		}
	}
	s := h.shell(c, info.Name, "/templates")
	s.PageKeys = "templates.hints.template"
	return web.Render(c, status, templatePage(s, v))
}

func (h *handler) templatePage(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	v := templateView{Saved: c.QueryParam("saved")}
	v.Version, _ = strconv.Atoi(c.QueryParam("v"))
	return h.renderTemplate(c, http.StatusOK, id, v)
}

func (h *handler) templateUpdate(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	f := detailsForm{Name: c.FormValue("name"), Slug: info.Slug, Description: c.FormValue("description")}
	if info.Latest == 0 {
		f.Slug = c.FormValue("slug")
	}
	err = h.Templates.Rename(ctx, id, f.Name, f.Slug, f.Description, "admin")
	switch {
	case err == nil:
		return web.Redirect(c, actionHref(id, "?saved=updated"))
	case errors.Is(err, templates.ErrSlugTaken):
		return h.renderTemplate(c, http.StatusUnprocessableEntity, id, templateView{Typed: &f, Errs: map[string]string{"slug": i18n.T(ctx, "templates.err.slug_taken")}})
	case errors.Is(err, templates.ErrSlugFrozen):
		return h.renderTemplate(c, http.StatusConflict, id, templateView{Typed: &f, Error: i18n.T(ctx, "templates.err.slug_frozen")})
	}
	if errs, ok := fieldErrors(c, err); ok {
		return h.renderTemplate(c, http.StatusUnprocessableEntity, id, templateView{Typed: &f, Errs: errs})
	}
	return notFound(err)
}

func (h *handler) templateArchive(c *echo.Context) error   { return h.archive(c, true) }
func (h *handler) templateUnarchive(c *echo.Context) error { return h.archive(c, false) }

func (h *handler) archive(c *echo.Context, archived bool) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	if err := h.Templates.Archive(c.Request().Context(), id, archived, "admin"); err != nil {
		return notFound(err)
	}
	saved := "unarchived"
	if archived {
		saved = "archived"
	}
	return web.Redirect(c, "/templates?saved="+saved)
}

func (h *handler) templateDelete(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	switch err := h.Templates.Delete(ctx, id, "admin"); {
	case errors.Is(err, templates.ErrInUse):
		return h.renderTemplate(c, http.StatusConflict, id, templateView{Error: i18n.T(ctx, "templates.err.in_use"), InUse: true})
	case err != nil:
		return notFound(err)
	}
	return web.Redirect(c, "/templates?saved=deleted")
}

// ---- versions ---------------------------------------------------------------

func (h *handler) versionDefault(c *echo.Context) error {
	id, n, err := idAndVersion(c)
	if err != nil {
		return err
	}
	if err := h.Templates.MakeDefault(c.Request().Context(), id, n, "admin"); err != nil {
		return notFound(err)
	}
	return web.Redirect(c, actionHref(id, "?saved=default&v="+strconv.Itoa(n)))
}

func (h *handler) versionDraft(c *echo.Context) error {
	id, n, err := idAndVersion(c)
	if err != nil {
		return err
	}
	if _, err := h.Templates.DraftFromVersion(c.Request().Context(), id, n, "admin"); err != nil {
		return notFound(err)
	}
	return web.Redirect(c, actionHref(id, "/edit"))
}

func (h *handler) versionExport(c *echo.Context) error {
	id, n, err := idAndVersion(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	v, err := h.Templates.Version(ctx, id, n)
	if err != nil {
		return notFound(err)
	}
	b, err := templates.Export(v)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-v%d.zip"`, info.Slug, n))
	return c.Blob(http.StatusOK, "application/zip", b)
}

func idAndVersion(c *echo.Context) (int64, int, error) {
	id, err := tplID(c)
	if err != nil {
		return 0, 0, err
	}
	n, err := versionNo(c)
	return id, n, err
}

type versionView struct {
	T        templates.Info
	V        templates.Version
	Versions []templates.VersionInfo
	Previous int // the version before this one, 0 for none
	File     string
	Paths    []string
	Lines    []ui.CodeLine
	Binary   bool
	Size     int
}

func (h *handler) versionView(c *echo.Context) error {
	id, n, err := idAndVersion(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	ver, err := h.Templates.Version(ctx, id, n)
	if err != nil {
		return notFound(err)
	}
	vs, err := h.Templates.Versions(ctx, id)
	if err != nil {
		return err
	}
	v := versionView{T: info, V: ver, Versions: vs, Paths: sortedPaths(ver.Files)}
	for _, x := range vs {
		if x.Number < n && x.Number > v.Previous {
			v.Previous = x.Number
		}
	}
	v.File = c.QueryParam("file")
	if _, ok := ver.Files[v.File]; !ok {
		v.File = manifest.Name
		if _, ok := ver.Files[v.File]; !ok && len(v.Paths) > 0 {
			v.File = v.Paths[0]
		}
	}
	if body, ok := ver.Files[v.File]; ok {
		v.Size = len(body)
		if isBinary(body) {
			v.Binary = true
		} else {
			v.Lines = withMarks(ui.Highlight(v.File, body), ver.Warnings, v.File)
		}
	}
	s := h.shell(c, fmt.Sprintf("%s v%d", info.Name, n), "/templates")
	s.PageKeys = "templates.hints.version_page"
	return web.Render(c, http.StatusOK, versionPage(s, v))
}

// withMarks puts findings under the lines they name.
func withMarks(lines []ui.CodeLine, fs []finding.Finding, path string) []ui.CodeLine {
	for _, f := range fs {
		if f.Path != path || f.Line < 1 || f.Line > len(lines) {
			continue
		}
		lines[f.Line-1].Marks = append(lines[f.Line-1].Marks, ui.CodeMark{Severity: string(f.Severity), Text: f.Message})
	}
	return lines
}

// ---- diff -------------------------------------------------------------------

type diffPageView struct {
	T         templates.Info
	Versions  []templates.VersionInfo
	A, B      int
	Split     bool
	Diff      ui.DiffView
	Unchanged int
	Same      bool
}

func (h *handler) diffPage(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	vs, err := h.Templates.Versions(ctx, id)
	if err != nil {
		return err
	}
	v := diffPageView{T: info, Versions: vs, Split: c.QueryParam("view") == "split"}
	if len(vs) > 0 {
		v.B = vs[0].Number
		if len(vs) > 1 {
			v.A = vs[1].Number
		}
	}
	if n, err := strconv.Atoi(c.QueryParam("b")); err == nil {
		v.B = n
	}
	if n, err := strconv.Atoi(c.QueryParam("a")); err == nil {
		v.A = n
	}
	if v.A > 0 && v.B > 0 {
		a, err := h.Templates.Version(ctx, id, v.A)
		if err != nil {
			return notFound(err)
		}
		b, err := h.Templates.Version(ctx, id, v.B)
		if err != nil {
			return notFound(err)
		}
		v.Same = v.A == v.B
		v.Diff = ui.DiffView{Split: v.Split}
		for _, d := range templates.Diff(a.Files, b.Files) {
			if d.Change == templates.Unchanged {
				v.Unchanged++
				continue
			}
			f := ui.DiffFile{Path: d.Path, Change: d.Change, Hunks: ui.ParseUnified(d.Unified)}
			if d.Binary {
				f.Note = i18n.T(ctx, "templates.diff.binary", i18n.Args{"old": d.OldSize, "new": d.NewSize})
			}
			v.Diff.Files = append(v.Diff.Files, f)
		}
	}
	s := h.shell(c, i18n.T(ctx, "templates.diff.title", i18n.Args{"name": info.Name}), "/templates")
	s.PageKeys = "templates.hints.template"
	return web.Render(c, http.StatusOK, diffTemplatePage(s, v))
}

func sortedPaths(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return pathLess(out[i], out[j]) })
	return out
}

// pathLess puts manifest.yaml first and sorts the rest by path, ignoring case.
func pathLess(a, b string) bool {
	if (a == manifest.Name) != (b == manifest.Name) {
		return a == manifest.Name
	}
	if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
		return la < lb
	}
	return a < b
}

// isBinary is a file the editor can't hold in a text area.
func isBinary(b []byte) bool { return !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 }

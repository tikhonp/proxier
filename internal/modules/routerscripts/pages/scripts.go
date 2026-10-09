package pages

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ------------------------------------------------------------------ the list

type listRow struct {
	ID                 int64
	Name, Description  string
	Current, Published string
	Generations, Draft string
	DraftErrors        bool
}

type listView struct {
	Rows     []listRow
	Archived bool // showing the archived ones
	NArch    int
}

func (h *handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	v := listView{Archived: c.QueryParam("archived") == "1"}
	rows, err := h.Scripts.List(ctx, v.Archived)
	if err != nil {
		return err
	}
	if v.NArch, err = h.Scripts.ArchivedCount(ctx); err != nil {
		return err
	}
	for _, r := range rows {
		row := listRow{
			ID: r.ID, Name: r.Name, Description: r.Description, Current: versionWord(ctx, r.Current),
			Generations: loc.Number(int64(r.Generations)), Draft: i18n.T(ctx, "scripts.draft.none"),
		}
		if !r.PublishedAt.IsZero() {
			row.Published = loc.Date(r.PublishedAt)
		}
		switch {
		case r.Draft && r.DraftErrors > 0:
			row.Draft, row.DraftErrors = loc.N("scripts.draft.problems", int64(r.DraftErrors)), true
		case r.Draft:
			row.Draft = loc.N("scripts.draft.params", int64(r.DraftParams))
		}
		v.Rows = append(v.Rows, row)
	}
	return web.Render(c, http.StatusOK, listPage(h.shell(c, i18n.T(ctx, "rscripts.title"), ListPath), v))
}

// ------------------------------------------------------------------ new script

type newView struct {
	Name, Slug, Description, Body string
	Errs                          map[string]string
}

func (h *handler) renderNew(c *echo.Context, code int, v newView) error {
	ctx := c.Request().Context()
	title := i18n.T(ctx, "scripts.new.title")
	return web.Render(c, code, newPage(h.shell(c, title, ListPath), v, title))
}

func (h *handler) newPage(c *echo.Context) error { return h.renderNew(c, http.StatusOK, newView{}) }

func (h *handler) create(c *echo.Context) error {
	ctx := c.Request().Context()
	// a pasted body gets LF (browsers send CRLF), an upload stays as it is
	body, _, err := formBody(c, "body", "")
	if err != nil {
		return err
	}
	v := newView{Name: c.FormValue("name"), Slug: c.FormValue("slug"), Description: c.FormValue("description"), Body: body}
	id, err := h.Scripts.Create(ctx, scripts.New{Name: v.Name, Slug: v.Slug, Description: v.Description, Body: body}, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderNew(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, ScriptHref(id)+"?created=1")
}

// ------------------------------------------------------------------ details

type detailsView struct {
	S                       scripts.Script
	Name, Slug, Description string
	Errs                    map[string]string
}

func (h *handler) renderDetails(c *echo.Context, code int, v detailsView) error {
	ctx := c.Request().Context()
	title := i18n.T(ctx, "scripts.details.title", i18n.Args{"name": v.S.Name})
	return web.Render(c, code, detailsPage(h.shell(c, title, ListPath), v, title))
}

func (h *handler) detailsPage(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	return h.renderDetails(c, http.StatusOK, detailsView{S: s, Name: s.Name, Slug: s.Slug, Description: s.Description})
}

func (h *handler) details(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := detailsView{S: s, Name: c.FormValue("name"), Slug: c.FormValue("slug"), Description: c.FormValue("description")}
	_, err = h.Scripts.Edit(ctx, s.ID, scripts.Details{Name: v.Name, Slug: v.Slug, Description: v.Description}, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderDetails(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, ScriptHref(s.ID))
}

// ------------------------------------------------------------------ archive, delete

func (h *handler) archive(archived bool) echo.HandlerFunc {
	return func(c *echo.Context) error {
		s, err := h.load(c)
		if err != nil {
			return err
		}
		if err := h.Scripts.Archive(c.Request().Context(), s.ID, archived, events.ActorAdmin); err != nil {
			return err
		}
		return web.Redirect(c, ScriptHref(s.ID))
	}
}

type deleteView struct {
	S           scripts.Script
	Generations int
	Typed       string
	Err         string
}

func (h *handler) renderDelete(c *echo.Context, code int, v deleteView) error {
	ctx := c.Request().Context()
	n, err := h.Scripts.Generations(ctx, v.S.ID)
	if err != nil {
		return err
	}
	v.Generations = n
	title := i18n.T(ctx, "scripts.delete.title", i18n.Args{"name": v.S.Name})
	return web.Render(c, code, deletePage(h.shell(c, title, ListPath), v, title))
}

func (h *handler) deletePage(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	return h.renderDelete(c, http.StatusOK, deleteView{S: s})
}

func (h *handler) deletePost(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := deleteView{S: s, Typed: c.FormValue("slug")}
	if strings.TrimSpace(v.Typed) != s.Slug {
		v.Err = i18n.T(ctx, "scripts.delete.mismatch", i18n.Args{"slug": s.Slug})
		return h.renderDelete(c, http.StatusUnprocessableEntity, v)
	}
	err = h.Scripts.Delete(ctx, s.ID, events.ActorAdmin)
	if errors.Is(err, scripts.ErrHasGenerations) {
		return h.renderDelete(c, http.StatusConflict, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, ListPath)
}

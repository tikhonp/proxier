package pages

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) registerTemplates(r web.Routes) {
	a := r.Admin
	a.GET("/templates", h.templatesList)
	a.GET("/templates/new", h.templateNew)
	a.POST("/templates", h.templateCreate)
	a.GET("/templates/import", h.importForm)
	a.POST("/templates/import", h.importDo)
	a.GET("/templates/:id", h.templatePage)
	a.POST("/templates/:id", h.templateUpdate)
	a.POST("/templates/:id/archive", h.templateArchive)
	a.POST("/templates/:id/unarchive", h.templateUnarchive)
	a.POST("/templates/:id/delete", h.templateDelete)
	a.GET("/templates/:id/versions/:n", h.versionView)
	a.POST("/templates/:id/versions/:n/default", h.versionDefault)
	a.POST("/templates/:id/versions/:n/draft", h.versionDraft)
	a.GET("/templates/:id/versions/:n/export", h.versionExport)
	a.GET("/templates/:id/diff", h.diffPage)
	a.GET("/templates/:id/edit", h.editor)
	a.POST("/templates/:id/draft", h.draftSave)
	a.POST("/templates/:id/draft/upload", h.draftUpload)
	a.POST("/templates/:id/draft/validate", h.draftValidate)
	a.POST("/templates/:id/draft/preview", h.draftPreview)
	a.POST("/templates/:id/draft/discard", h.draftDiscard)
	a.GET("/templates/:id/publish", h.publishPage)
	a.POST("/templates/:id/publish", h.publishDo)
}

// tplID reads :id.
func tplID(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

// versionNo reads :n.
func versionNo(c *echo.Context) (int, error) {
	n, err := strconv.Atoi(c.Param("n"))
	if err != nil || n < 1 {
		return 0, echo.ErrNotFound
	}
	return n, nil
}

// notFound turns the store's and service's missing-row errors into a 404.
func notFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return echo.ErrNotFound
	}
	return err
}

// ---- list -------------------------------------------------------------------

type templatesView struct {
	Items         []templates.Summary
	Archived      bool // the "archived" filter is on
	ArchivedCount int
	Saved         string
}

func (h *handler) templatesList(c *echo.Context) error {
	ctx := c.Request().Context()
	archived := c.QueryParam("archived") == "1"
	all, err := h.Templates.List(ctx, true)
	if err != nil {
		return err
	}
	v := templatesView{Archived: archived, Saved: c.QueryParam("saved")}
	for _, t := range all {
		if t.Archived {
			v.ArchivedCount++
		}
		if t.Archived == archived {
			v.Items = append(v.Items, t)
		}
	}
	s := h.shell(c, i18n.T(ctx, "templates.title"), "/templates")
	s.PageKeys = "templates.hints.list"
	return web.Render(c, http.StatusOK, templatesPage(s, v))
}

// ---- new --------------------------------------------------------------------

type newTemplateView struct {
	Name, Slug, Description string
	Errs                    map[string]string
}

func (h *handler) templateNew(c *echo.Context) error {
	return h.renderNew(c, http.StatusOK, newTemplateView{})
}

func (h *handler) renderNew(c *echo.Context, status int, v newTemplateView) error {
	ctx := c.Request().Context()
	s := h.shell(c, i18n.T(ctx, "templates.new"), "/templates")
	return web.Render(c, status, newTemplatePage(s, v))
}

func (h *handler) templateCreate(c *echo.Context) error {
	v := newTemplateView{Name: c.FormValue("name"), Slug: c.FormValue("slug"), Description: c.FormValue("description")}
	slug := v.Slug
	if slug == "" {
		slug = templates.Slugify(v.Name)
	}
	id, err := h.Templates.Create(c.Request().Context(), v.Name, slug, v.Description, "admin")
	switch {
	case errors.Is(err, templates.ErrSlugTaken):
		v.Errs = map[string]string{"slug": i18n.T(c.Request().Context(), "templates.err.slug_taken")}
		return h.renderNew(c, http.StatusUnprocessableEntity, v)
	case err != nil:
		if errs, ok := fieldErrors(c, err); ok {
			v.Errs = errs
			return h.renderNew(c, http.StatusUnprocessableEntity, v)
		}
		return err
	}
	return web.Redirect(c, "/templates/"+strconv.FormatInt(id, 10)+"/edit")
}

// actionHref is a template-relative URL.
func actionHref(id int64, rest string) string {
	return "/templates/" + strconv.FormatInt(id, 10) + rest
}

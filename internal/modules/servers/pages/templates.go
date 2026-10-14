package pages

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) registerTemplates(r web.Routes) {
	srv := r.Area(access.Servers)
	srv.Manage.GET("/templates", h.templatesList)
	srv.Manage.GET("/templates/new", h.templateNew)
	srv.Manage.POST("/templates", h.templateCreate)
	srv.Manage.GET("/templates/import", h.importForm)
	srv.Manage.POST("/templates/import", h.importDo)
	srv.Manage.GET("/templates/:id", h.templatePage)
	srv.Manage.POST("/templates/:id", h.templateUpdate)
	srv.Manage.POST("/templates/:id/archive", h.templateArchive)
	srv.Manage.POST("/templates/:id/unarchive", h.templateUnarchive)
	srv.Manage.POST("/templates/:id/delete", h.templateDelete)
	srv.Manage.GET("/templates/:id/versions/:n", h.versionView)
	srv.Manage.POST("/templates/:id/versions/:n/default", h.versionDefault)
	srv.Manage.POST("/templates/:id/versions/:n/draft", h.versionDraft)
	srv.Manage.GET("/templates/:id/versions/:n/export", h.versionExport)
	srv.Manage.GET("/templates/:id/diff", h.diffPage)
	srv.Manage.GET("/templates/:id/edit", h.editor)
	srv.Manage.POST("/templates/:id/draft", h.draftSave)
	srv.Manage.POST("/templates/:id/draft/upload", h.draftUpload)
	srv.Manage.POST("/templates/:id/draft/validate", h.draftValidate)
	srv.Manage.POST("/templates/:id/draft/preview", h.draftPreview)
	srv.Manage.POST("/templates/:id/draft/discard", h.draftDiscard)
	srv.Manage.GET("/templates/:id/publish", h.publishPage)
	srv.Manage.POST("/templates/:id/publish", h.publishDo)
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
	id, err := h.Templates.Create(c.Request().Context(), v.Name, slug, v.Description, web.Actor(c.Request().Context()))
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

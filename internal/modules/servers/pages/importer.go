package pages

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type importTarget struct {
	ID       int64
	Name     string
	Slug     string
	HasDraft bool
}

type importView struct {
	Targets []importTarget
	Source  string // zip | git
	Into    string // "new" or a template id
	Name    string
	Slug    string
	URL     string
	Ref     string
	Path    string
	Confirm bool
	Error   string            // translated, for the whole form
	Errs    map[string]string // translated, by field
}

func (h *handler) renderImport(c *echo.Context, status int, v importView) error {
	ctx := c.Request().Context()
	all, err := h.Templates.List(ctx, false)
	if err != nil {
		return err
	}
	for _, t := range all {
		v.Targets = append(v.Targets, importTarget{ID: t.ID, Name: t.Name, Slug: t.Slug, HasDraft: t.HasDraft})
	}
	if v.Source == "" {
		v.Source = "zip"
	}
	if v.Into == "" {
		v.Into = "new"
	}
	s := h.shell(c, i18n.T(ctx, "templates.import.title"), "/templates")
	return web.Render(c, status, importPage(s, v))
}

func (h *handler) importForm(c *echo.Context) error {
	v := importView{Into: c.QueryParam("into"), Source: c.QueryParam("source")}
	return h.renderImport(c, http.StatusOK, v)
}

// importErrKey is the message key of an import refusal.
func importErrKey(err error) string {
	switch {
	case errors.Is(err, templates.ErrNotZip):
		return "templates.err.not_zip"
	case errors.Is(err, templates.ErrManifestNotFound):
		return "templates.err.no_manifest"
	case errors.Is(err, templates.ErrUnsafePath):
		return "templates.err.unsafe"
	case errors.Is(err, templates.ErrTooLarge):
		return "templates.err.zip_large"
	case errors.Is(err, templates.ErrTooManyFiles):
		return "templates.err.too_many"
	case errors.Is(err, templates.ErrGitURL):
		return "templates.err.git_url"
	case errors.Is(err, templates.ErrGitRefNotFound):
		return "templates.err.git_ref"
	case errors.Is(err, templates.ErrGitPathNotFound):
		return "templates.err.git_path"
	case errors.Is(err, templates.ErrGitTooLarge):
		return "templates.err.git_large"
	}
	return ""
}

func (h *handler) importDo(c *echo.Context) error {
	ctx := c.Request().Context()
	req := c.Request()
	if err := req.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return h.renderImport(c, http.StatusRequestEntityTooLarge, importView{Error: i18n.T(ctx, "templates.err.too_large")})
	}
	v := importView{
		Source: req.PostFormValue("source"), Into: req.PostFormValue("into"),
		Name: strings.TrimSpace(req.PostFormValue("name")), Slug: strings.TrimSpace(req.PostFormValue("slug")),
		URL: strings.TrimSpace(req.PostFormValue("url")), Ref: strings.TrimSpace(req.PostFormValue("ref")),
		Path: strings.TrimSpace(req.PostFormValue("path")), Confirm: req.PostFormValue("confirm") == "1",
	}
	fail := func(status int, err error, field string) error {
		msg := ""
		if key := importErrKey(err); key != "" {
			msg = i18n.T(ctx, key)
		} else if status < 500 {
			msg = err.Error()
		} else {
			return err
		}
		if field != "" {
			v.Errs = map[string]string{field: msg}
		} else {
			v.Error = msg
		}
		return h.renderImport(c, status, v)
	}

	into := importTarget{}
	if v.Into != "" && v.Into != "new" {
		id, err := strconv.ParseInt(v.Into, 10, 64)
		if err != nil {
			return echo.ErrBadRequest
		}
		info, err := h.Templates.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		into = importTarget{ID: id, Name: info.Name, HasDraft: info.HasDraft}
		if info.HasDraft && !v.Confirm {
			v.Error = i18n.T(ctx, "templates.import.confirm_needed", i18n.Args{"name": info.Name})
			return h.renderImport(c, http.StatusConflict, v)
		}
	}

	var files map[string][]byte
	var src templates.Source
	switch v.Source {
	case "git":
		if v.URL == "" {
			v.Errs = map[string]string{"url": i18n.T(ctx, "templates.err.git_url")}
			return h.renderImport(c, http.StatusUnprocessableEntity, v)
		}
		f, commit, err := h.Templates.FetchGit(ctx, templates.GitSource{URL: v.URL, Ref: v.Ref, Path: v.Path})
		if err != nil {
			if importErrKey(err) == "" {
				return fail(http.StatusBadGateway, errors.New(i18n.T(ctx, "templates.err.git_fetch")+" "+err.Error()), "url")
			}
			return fail(http.StatusUnprocessableEntity, err, "url")
		}
		files, src = f, templates.Source{Kind: "git", URL: v.URL, Ref: v.Ref, Commit: commit, Path: strings.Trim(v.Path, "/")}
	default:
		v.Source = "zip"
		f, hdr, err := req.FormFile("zip")
		if err != nil {
			v.Errs = map[string]string{"zip": i18n.T(ctx, "templates.err.no_file")}
			return h.renderImport(c, http.StatusUnprocessableEntity, v)
		}
		defer func() { _ = f.Close() }()
		files, err = templates.ReadZip(f, hdr.Size)
		if err != nil {
			return fail(http.StatusUnprocessableEntity, err, "zip")
		}
		src = templates.Source{Kind: "zip", Name: hdr.Filename}
	}

	target := templates.ImportTarget{TemplateID: into.ID}
	if into.ID == 0 {
		target.Name, target.Slug = v.Name, v.Slug
		if target.Name == "" {
			target.Name = templates.ManifestName(files)
		}
		if target.Slug == "" {
			target.Slug = templates.Slugify(target.Name)
		}
		v.Name, v.Slug = target.Name, target.Slug
	}
	id, err := h.Templates.Import(ctx, target, files, src, "admin")
	switch {
	case errors.Is(err, templates.ErrSlugTaken):
		v.Errs = map[string]string{"slug": i18n.T(ctx, "templates.err.slug_taken")}
		return h.renderImport(c, http.StatusUnprocessableEntity, v)
	case err != nil:
		var fe store.FieldErrors
		if errors.As(err, &fe) {
			errs, _ := fieldErrors(c, err)
			v.Errs = errs
			return h.renderImport(c, http.StatusUnprocessableEntity, v)
		}
		if key := importErrKey(err); key != "" {
			return fail(http.StatusUnprocessableEntity, err, "")
		}
		return notFound(err)
	}
	return web.Redirect(c, actionHref(id, "/edit"))
}

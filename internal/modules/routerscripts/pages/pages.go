// Package pages holds the router scripts module's handlers and templ files.
package pages

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Deps are what the pages use.
type Deps struct {
	Scripts     *scripts.Service
	Generations *generations.Service
	DB          *db.DB
	Now         func() time.Time
}

type handler struct {
	Deps
	shell func(c *echo.Context, title, path string) ui.Shell
}

// ListPath is the scripts list.
const ListPath = "/router-scripts"

// ScriptHref is a script's page.
func ScriptHref(id int64) string { return ListPath + "/" + strconv.FormatInt(id, 10) }

// Register adds the module's routes.
func Register(r web.Routes, d Deps) {
	h := &handler{Deps: d, shell: r.Shell}
	r.Admin.GET(ListPath, h.list)
	r.Admin.GET(ListPath+"/new", h.newPage)
	r.Admin.POST(ListPath, h.create)
	r.Admin.GET(ListPath+"/:id", h.page)
	r.Admin.GET(ListPath+"/:id/details", h.detailsPage)
	r.Admin.POST(ListPath+"/:id/details", h.details)
	r.Admin.POST(ListPath+"/:id/edit", h.edit)
	r.Admin.POST(ListPath+"/:id/draft", h.saveDraft)
	r.Admin.POST(ListPath+"/:id/draft/parameters", h.panel)
	r.Admin.POST(ListPath+"/:id/draft/discard", h.discard)
	r.Admin.GET(ListPath+"/:id/publish", h.publishPage)
	r.Admin.POST(ListPath+"/:id/publish", h.publish)
	r.Admin.GET(ListPath+"/:id/versions/:n", h.version)
	r.Admin.GET(ListPath+"/:id/versions/:n/download", h.download)
	r.Admin.POST(ListPath+"/:id/versions/:n/current", h.makeCurrent)
	r.Admin.GET(ListPath+"/:id/diff", h.diff)
	r.Admin.POST(ListPath+"/:id/archive", h.archive(true))
	r.Admin.POST(ListPath+"/:id/unarchive", h.archive(false))
	r.Admin.GET(ListPath+"/:id/generate", h.generatePage)
	r.Admin.POST(ListPath+"/:id/generate/summary", h.generateSummary)
	r.Admin.POST(ListPath+"/:id/generate", h.generate)
	r.Admin.GET(ListPath+"/generations/:gid", h.generationPage)
	r.Admin.GET(ListPath+"/generations/:gid/download", h.generationDownload)
	r.Admin.GET(ListPath+"/generations/:gid/changes", h.generationChanges)
	r.Admin.POST(ListPath+"/generations/:gid/fetch-url", h.fetchURLCreate)
	r.Admin.GET(ListPath+"/generations/:gid/fetch-url/reveal", h.fetchURLReveal)
	r.Admin.GET(ListPath+"/generations/:gid/after", h.afterArea)
	r.Admin.GET(ListPath+"/:id/delete", h.deletePage)
	r.Admin.POST(ListPath+"/:id/delete", h.deletePost)
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }
func itoa(n int) string  { return strconv.Itoa(n) }

// load reads the script of the :id parameter; 404 when there is none.
func (h *handler) load(c *echo.Context) (scripts.Script, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return scripts.Script{}, echo.ErrNotFound
	}
	s, err := h.Scripts.Get(c.Request().Context(), id)
	if errors.Is(err, scripts.ErrNotFound) {
		return s, echo.ErrNotFound
	}
	return s, err
}

// errText translates field errors (i18n keys) for a form.
func errText(ctx context.Context, fe store.FieldErrors) map[string]string {
	out := map[string]string{}
	for k, v := range fe {
		out[k] = i18n.T(ctx, v)
	}
	return out
}

// findingText is a finding in the admin's language.
func findingText(ctx context.Context, f params.Finding) string {
	return i18n.T(ctx, f.Key, i18n.Args(plainArgs(f.Args)))
}

// plainArgs writes numbers as they are: a line number read back from JSON
// is a float64, and 1234 must not become "1,234".
func plainArgs(a map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		switch n := v.(type) {
		case float64:
			out[k] = strconv.FormatFloat(n, 'f', -1, 64)
		case int:
			out[k] = strconv.Itoa(n)
		default:
			out[k] = v
		}
	}
	return out
}

// formBody is the posted body: an uploaded file wins and is kept byte for
// byte; a text area's text gets the line endings of what it replaces.
func formBody(c *echo.Context, field, stored string) (string, bool, error) {
	fh, err := c.FormFile("file")
	if err == nil && fh.Size > 0 {
		f, err := fh.Open()
		if err != nil {
			return "", false, err
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(io.LimitReader(f, scripts.MaxBody+1))
		return string(b), true, err
	}
	return scripts.Normalize(c.FormValue(field), stored), false, nil
}

type eventLine struct{ Time, Text string }

// activity is the last 10 events of a script, as sentences.
func (h *handler) activity(ctx context.Context, s scripts.Script) ([]eventLine, error) {
	loc := i18n.From(ctx)
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: scripts.Subject(s.ID), Limit: 10})
	if err != nil {
		return nil, err
	}
	out := make([]eventLine, 0, len(list))
	for _, e := range list {
		args := i18n.Args{"subject": s.Name, "actor": e.Actor}
		for k, v := range e.Payload {
			args[k] = v
		}
		key := "event." + e.Type
		if e.Type == "routerscript.archived" {
			key = "scripts.event.unarchived"
			if b, _ := e.Payload["archived"].(bool); b {
				key = "scripts.event.archived"
			}
		}
		text := e.Type
		if loc.Has(key) {
			text = loc.T(key, i18n.Args(plainArgs(args)))
		}
		out = append(out, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return out, nil
}

// versionWord is "v4" or "no version yet".
func versionWord(ctx context.Context, n int) string {
	if n == 0 {
		return i18n.T(ctx, "scripts.no_version")
	}
	return "v" + itoa(n)
}

// annotationsText writes a parameter's annotations as the script does.
func annotationsText(a params.Annotations) string {
	var out []string
	if a.Fill != "" {
		out = append(out, "@fill "+a.Fill)
	}
	if a.Secret {
		out = append(out, "@secret")
	}
	if a.Required {
		out = append(out, "@required")
	}
	if len(a.Choices) > 0 {
		out = append(out, "@choices "+strings.Join(a.Choices, "|"))
	}
	if a.Pattern != "" {
		out = append(out, "@pattern "+a.Pattern)
	}
	return strings.Join(out, " ")
}

// notFoundOr turns the service's lookup errors into 404.
func notFoundOr(err error) error {
	if errors.Is(err, scripts.ErrNotFound) || errors.Is(err, scripts.ErrNoVersion) || errors.Is(err, scripts.ErrNoDraft) {
		return echo.ErrNotFound
	}
	return err
}

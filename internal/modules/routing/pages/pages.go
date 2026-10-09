// Package pages holds the routing module's handlers and templ files.
package pages

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/mtvpn"
	"github.com/tikhonp/proxier/internal/modules/routing/refresh"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Deps are what the pages use.
type Deps struct {
	Services *services.Service
	Lists    *lists.Service
	Refresh  *refresh.Service
	Catalog  *catalog.Service
	// Shadowrocket and Import are 3d's.
	Shadowrocket *shadowrocket.Service
	Import       *mtvpn.Service
	Jobs         *jobs.System
	Settings     *settings.Store
	DB           *db.DB
	Now          func() time.Time
}

type handler struct {
	Deps
	shell         func(c *echo.Context, title, path string) ui.Shell
	settingsPages func() []ui.SettingsPage
}

// Register adds the module's routes.
func Register(r web.Routes, d Deps) {
	h := &handler{Deps: d, shell: r.Shell, settingsPages: r.SettingsPages}
	r.Admin.GET("/routing/services", h.list)
	r.Admin.GET("/routing/services/add", h.addPage)
	r.Admin.POST("/routing/services", h.add)
	r.Admin.GET("/routing/services/new", h.newPage)
	r.Admin.POST("/routing/services/custom", h.create)
	r.Admin.GET("/routing/services/:id", h.page)
	r.Admin.GET("/routing/services/:id/domains", h.domains)
	r.Admin.GET("/routing/services/:id/edit", h.editPage)
	r.Admin.POST("/routing/services/:id/edit", h.edit)
	r.Admin.GET("/routing/services/:id/switch", h.switchPage)
	r.Admin.POST("/routing/services/:id/switch", h.switchPost)
	r.Admin.GET("/routing/services/:id/snapshots/:snap", h.diff)
	r.Admin.GET("/routing/services/:id/remove", h.removePage)
	r.Admin.POST("/routing/services/:id/remove", h.removePost)
	r.Admin.POST("/routing/services/:id/refresh", h.refreshPost)
	r.Admin.GET("/routing/services/:id/source", h.sourceArea)
	r.Admin.POST("/routing/services/:id/snapshots/:snap/accept", h.acceptPost)
	r.Admin.POST("/routing/services/:id/snapshots/:snap/dismiss", h.dismissPost)
	r.Admin.POST("/routing/lists/:id/refresh", h.refreshListPost)
	r.Admin.GET("/routing/search", h.search)
	r.Admin.GET("/routing/search/preview", h.preview)
	r.Admin.POST("/routing/catalog/refresh", h.catalogRefresh)
	r.Admin.GET("/settings/routing", h.settingsPage)
	r.Admin.POST("/settings/routing", h.saveSettings)
	h.registerLists(r)
	h.registerShadowrocket(r)
	h.registerImport(r)
}

const listPath = "/routing/services"

func svcHref(id int64) string { return listPath + "/" + i64(id) }

func i64(n int64) string { return strconv.FormatInt(n, 10) }

func htmx(c *echo.Context) bool { return c.Request().Header.Get("HX-Request") == "true" }

// load reads the service of the :id parameter; 404 when there is none.
func (h *handler) load(c *echo.Context) (services.Item, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return services.Item{}, echo.ErrNotFound
	}
	it, err := h.Services.Get(c.Request().Context(), id)
	if errors.Is(err, services.ErrNotFound) {
		return it, echo.ErrNotFound
	}
	return it, err
}

// errText translates field errors (i18n keys) for the form.
func errText(ctx context.Context, fe store.FieldErrors) map[string]string {
	out := map[string]string{}
	for k, v := range fe {
		out[k] = i18n.T(ctx, v)
	}
	return out
}

// quoted is the 'name' an error message carries.
func quoted(err error) string {
	s := err.Error()
	i := strings.IndexByte(s, '\'')
	j := strings.LastIndexByte(s, '\'')
	if i < 0 || j <= i {
		return ""
	}
	return s[i+1 : j]
}

// selectorError says why a selector can't be added or switched to, in the
// admin's language; ok is false for errors that aren't the admin's to fix.
func selectorError(ctx context.Context, raw string, err error) (string, bool) {
	var (
		tt *services.SwitchTagError
		ue *sources.UnreachableError
		he *sources.HTTPError
		ie *sources.IncludeError
		ee *services.EmptyResolveError
	)
	switch {
	case errors.Is(err, selector.ErrEmpty):
		return i18n.T(ctx, "services.err.empty"), true
	case errors.Is(err, selector.ErrBadURL):
		return i18n.T(ctx, "services.err.bad_url"), true
	case errors.Is(err, selector.ErrBadName):
		return i18n.T(ctx, "services.err.bad_name"), true
	case errors.Is(err, selector.ErrUnknownPortal):
		return i18n.T(ctx, "services.err.portal", i18n.Args{"portal": quoted(err)}), true
	case errors.Is(err, selector.ErrUnknownSource):
		return i18n.T(ctx, "services.err.source", i18n.Args{"source": quoted(err)}), true
	case errors.Is(err, selector.ErrBadTag):
		return i18n.T(ctx, "services.err.bad_tag"), true
	case errors.As(err, &tt):
		return i18n.T(ctx, "services.err.switch_tag", i18n.Args{"selector": strings.TrimSpace(raw), "got": tt.Got, "want": tt.Want}), true
	case errors.As(err, &ee):
		return i18n.N(ctx, "services.err.empty_resolve", int64(ee.Skipped), i18n.Args{"selector": ee.Selector}), true
	case errors.As(err, &ue):
		if len(ue.Missed) == 0 {
			return i18n.T(ctx, "services.err.unreachable", i18n.Args{"unreachable": strings.Join(ue.Unreachable, ", ")}), true
		}
		return i18n.T(ctx, "services.err.missed_unreachable", i18n.Args{
			"missed": strings.Join(ue.Missed, ", "), "unreachable": strings.Join(ue.Unreachable, ", "),
		}), true
	case errors.As(err, &ie):
		return i18n.T(ctx, "services.err.fetch", i18n.Args{"error": ie.Error()}), true
	case errors.Is(err, sources.ErrNotFound):
		sel, _ := selector.Parse(raw)
		if sel.Source == selector.V2fly {
			return i18n.T(ctx, "services.err.v2fly_missing", i18n.Args{"name": sel.Name}), true
		}
		portals := selector.Portals
		if sel.Portal != "" {
			portals = []string{sel.Portal}
		}
		return i18n.T(ctx, "services.err.iplist_missing", i18n.Args{"portals": strings.Join(portals, ", ")}), true
	case errors.As(err, &he):
		return i18n.T(ctx, "services.err.http", i18n.Args{"url": he.URL, "status": he.Status}), true
	case errors.Is(err, sources.ErrTooBig):
		return i18n.T(ctx, "services.err.too_big"), true
	case errors.Is(err, context.DeadlineExceeded):
		return i18n.T(ctx, "services.err.timeout"), true
	}
	if ctx.Err() != nil {
		return "", false
	}
	// transport errors: the upstream, not Proxier, failed
	return i18n.T(ctx, "services.err.fetch", i18n.Args{"error": err.Error()}), true
}

// sourceWord is how a service's source is shown.
func sourceWord(ctx context.Context, it services.Item) string {
	if it.Source == selector.Custom {
		if it.Origin != "" {
			return i18n.T(ctx, "services.custom_from."+it.Origin)
		}
		return i18n.T(ctx, "services.custom")
	}
	return it.Selector
}

// inLists is "in no list" or "in Main and Parents".
func inLists(ctx context.Context, lists []string) string {
	switch len(lists) {
	case 0:
		return i18n.T(ctx, "services.in_no_list")
	case 1:
		return i18n.T(ctx, "services.in_lists", i18n.Args{"lists": lists[0]})
	}
	return i18n.T(ctx, "services.in_lists", i18n.Args{
		"lists": strings.Join(lists[:len(lists)-1], ", ") + " " + i18n.T(ctx, "services.and") + " " + lists[len(lists)-1],
	})
}

// pageOf is the 1-based ?page= parameter.
func pageOf(c *echo.Context) int {
	n, err := strconv.Atoi(c.QueryParam("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// perPage is the size of a page of services or domains.
const perPage = 50

// window is the slice bounds of page p of n items.
func window(n, p int) (from, to int) {
	from = (p - 1) * perPage
	if from > n {
		from = n
	}
	to = from + perPage
	if to > n {
		to = n
	}
	return from, to
}

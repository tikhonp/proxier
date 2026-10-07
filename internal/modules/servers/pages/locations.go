// Package pages holds the servers module's admin pages. A page builds its
// layout data with web.Routes.Shell and renders with web.Render.
package pages

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/vault"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Deps is what the pages need.
type Deps struct {
	Store     *store.Store
	Templates *templates.Service
	Log       *slog.Logger
	Settings  *settings.Store
	// DNS answers whether a hostname is in an allowed zone; CloudflareClient
	// makes the client of Settings → Integrations → Cloudflare (tests point it
	// at cloudflaretest).
	DNS              dns.Driver
	CloudflareClient func(token string) *cloudflare.Client
	// Vault opens endpoint credentials; Jobs shows and cancels a server's
	// provisioning; Provision is the new-server form and its actions.
	Vault     *vault.Vault
	Jobs      *jobs.System
	Provision *provision.Service
	// Deploy plans and runs changes to active servers (1e).
	Deploy *deploy.Service
	// Usage is the subscriptions port (Phase 2); it returns nil until then.
	Usage func() UsageReader
}

type handler struct {
	Deps
	shell         func(c *echo.Context, title, path string) ui.Shell
	SettingsPages func() []ui.SettingsPage
}

// Register adds the module's routes.
func Register(r web.Routes, d Deps) {
	h := &handler{Deps: d, shell: r.Shell, SettingsPages: r.SettingsPages}
	r.Admin.GET("/settings/servers", h.serversSettings)
	r.Admin.POST("/settings/servers", h.saveServersSettings)
	r.Admin.GET("/settings/integrations/cloudflare", h.cloudflarePage)
	r.Admin.POST("/settings/integrations/cloudflare/token", h.cloudflareToken)
	r.Admin.POST("/settings/integrations/cloudflare/zones", h.cloudflareZones)
	r.Admin.POST("/settings/integrations/cloudflare/test", h.cloudflareTest)
	r.Admin.GET("/locations", h.locations)
	r.Admin.POST("/locations", h.createLocation)
	r.Admin.POST("/locations/:id", h.updateLocation)
	r.Admin.POST("/locations/:id/delete", h.deleteLocation)
	h.registerTemplates(r)
	h.registerServers(r)
	h.registerDeploy(r)
}

type locForm struct{ Code, Name, Country string }

type locationsView struct {
	Locations []store.Location
	Countries []country.Entry
	Edit      int64             // the row being edited, 0 for none
	New       locForm           // the add form as typed
	Typed     locForm           // the edit form as typed
	Errs      map[string]string // translated, by field
	Saved     string            // created | updated | deleted
	Error     string            // translated, for the whole page
}

func (h *handler) render(c *echo.Context, status int, v locationsView) error {
	ctx := c.Request().Context()
	locs, err := h.Store.Locations(ctx)
	if err != nil {
		return err
	}
	v.Locations = locs
	v.Countries = country.List(string(i18n.From(ctx).Lang))
	s := h.shell(c, i18n.T(ctx, "locations.title"), "/locations")
	return web.Render(c, status, locationsPage(s, v))
}

func (h *handler) locations(c *echo.Context) error {
	v := locationsView{Saved: c.QueryParam("saved")}
	if id, err := strconv.ParseInt(c.QueryParam("edit"), 10, 64); err == nil {
		v.Edit = id
	}
	return h.render(c, http.StatusOK, v)
}

// fieldErrors translates a store.FieldErrors, if err is one.
func fieldErrors(c *echo.Context, err error) (map[string]string, bool) {
	var fe store.FieldErrors
	if !errors.As(err, &fe) {
		return nil, false
	}
	ctx := c.Request().Context()
	out := make(map[string]string, len(fe))
	for k, key := range fe {
		out[k] = i18n.T(ctx, key)
	}
	return out, true
}

func (h *handler) createLocation(c *echo.Context) error {
	f := locForm{Code: c.FormValue("code"), Name: c.FormValue("name"), Country: c.FormValue("country")}
	_, err := h.Store.CreateLocation(c.Request().Context(), f.Code, f.Name, f.Country, "admin")
	if errs, ok := fieldErrors(c, err); ok {
		return h.render(c, http.StatusUnprocessableEntity, locationsView{New: f, Errs: errs})
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, "/locations?saved=created")
}

func (h *handler) id(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

func (h *handler) updateLocation(c *echo.Context) error {
	id, err := h.id(c)
	if err != nil {
		return err
	}
	f := locForm{Name: c.FormValue("name"), Country: c.FormValue("country")}
	err = h.Store.UpdateLocation(c.Request().Context(), id, f.Name, f.Country, "admin")
	if errors.Is(err, store.ErrNotFound) {
		return echo.ErrNotFound
	}
	if errs, ok := fieldErrors(c, err); ok {
		return h.render(c, http.StatusUnprocessableEntity, locationsView{Edit: id, Typed: f, Errs: errs})
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, "/locations?saved=updated")
}

func (h *handler) deleteLocation(c *echo.Context) error {
	id, err := h.id(c)
	if err != nil {
		return err
	}
	switch err := h.Store.DeleteLocation(c.Request().Context(), id, "admin"); {
	case errors.Is(err, store.ErrNotFound):
		return echo.ErrNotFound
	case errors.Is(err, store.ErrLocationInUse):
		return h.render(c, http.StatusConflict, locationsView{Error: i18n.T(c.Request().Context(), "locations.err.in_use")})
	case err != nil:
		return err
	}
	return web.Redirect(c, "/locations?saved=deleted")
}

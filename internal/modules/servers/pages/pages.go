// Package pages holds the servers module's admin pages. A page builds its
// layout data with web.Routes.Shell and renders with web.Render.
package pages

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/agent"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/retire"
	"github.com/tikhonp/proxier/internal/modules/servers/stats"
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
	// Health runs and reads the checks; Stats answers the Stats tab (1f).
	Health *health.Service
	Stats  *stats.Service
	// Retire takes servers out of service (1g).
	Retire *retire.Service
	// Agent hands template drafts to coding agents (1h).
	Agent *agent.Service
	// Usage is the subscriptions port (Phase 2); it returns nil until then.
	Usage func() UsageReader
}

type handler struct {
	Deps
	forms         usedForms // hand-off forms already posted (agent.go)
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
	h.registerTemplates(r)
	h.registerServers(r)
	h.registerDeploy(r)
	h.registerHealth(r)
	h.registerRetire(r)
	h.registerAgent(r)
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

// id is the :id of the route, 404 when it is not a number.
func (h *handler) id(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

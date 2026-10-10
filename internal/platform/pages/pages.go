// Package pages holds the platform's own admin pages: sign-in, dashboard,
// settings, search. Other modules add theirs through module.RouteDeclarer.
package pages

import (
	"context"
	"log/slog"
	"sort"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/backup"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Searcher is module.Searcher: things for the search pop-up's "Go to".
type Searcher interface {
	Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error)
}

// Integrator is a module with rows on Settings → Integrations.
type Integrator interface {
	Integrations(ctx context.Context) []ui.IntegrationRow
}

// Dashboarder is a module with areas on the dashboard.
type Dashboarder interface {
	Dashboard(ctx context.Context) ([]ui.DashboardArea, error)
}

// Deps is what the handlers need. Nav, SettingsPages and Searchers are
// collected from every module.
type Deps struct {
	Log           *slog.Logger
	Cfg           *config.Config
	Auth          *auth.Service
	Settings      *settings.Store
	I18n          *i18n.Catalog
	Nav           []ui.NavItem
	SettingsPages []ui.SettingsPage
	Searchers     []Searcher
	// Integrators add rows to Settings → Integrations after the platform's own.
	Integrators []Integrator
	// Dashboards add areas to the dashboard after the platform's own.
	Dashboards []Dashboarder
	Jobs       *jobs.System
	Query      sqlx.QueryerContext // the read pool, for Activity
	Events     *events.Catalog
	Notify     *notify.Service
	Telegram   *telegram.Channel
	SSH        *sshx.SSH
	Tailnet    *tailnet.Node
	Backup     *backup.Service
	Namers     []Namer
	// Closing is closed when the server starts shutting down; live streams
	// end then.
	Closing <-chan struct{}
}

type handler struct {
	Deps
	secure bool
	names  *subjectNames
}

// Register adds the platform's routes to r and returns the builder of the
// layout's data, which modules' pages use through web.Routes.Shell.
func Register(r web.Routes, d Deps) func(c *echo.Context, title, path string) ui.Shell {
	h := &handler{Deps: d, secure: d.Cfg.BaseURL != nil && d.Cfg.BaseURL.Scheme == "https",
		names: newSubjectNames(d.Log, d.Settings, d.Namers)}

	r.Open.GET("/login", h.loginPage)
	// a HEAD that matches no route meets the session check, which sends it to
	// /login: the page itself must answer HEAD, or that is a redirect loop
	r.Open.HEAD("/login", h.loginPage)
	r.Open.POST("/login", h.login)
	r.Admin.POST("/logout", h.logout)

	r.Admin.GET("/", h.dashboard)
	r.Admin.GET("/search", h.search)

	r.Admin.GET("/jobs", h.jobsPage)
	r.Admin.GET("/jobs/cell", h.jobsCell)
	r.Admin.POST("/jobs/demo", h.runDemo)
	r.Admin.GET("/jobs/:id", h.jobPage)
	r.Admin.GET("/jobs/:id/stream", h.jobStream)
	r.Admin.GET("/jobs/:id/log.txt", h.jobLogText)
	r.Admin.POST("/jobs/:id/cancel", h.jobCancel)
	r.Admin.POST("/jobs/:id/retry", h.jobRetry)
	r.Admin.GET("/activity", h.activityPage)
	r.Admin.POST("/me/language", h.setLanguage)

	r.Admin.GET("/settings/integrations", h.integrationsPage)
	r.Admin.GET("/settings/integrations/tailnet", h.tailnetPage)
	r.Admin.POST("/settings/integrations/tailnet/reauth", h.tailnetReauth)
	r.Admin.GET("/settings/integrations/telegram", h.telegramPage)
	r.Admin.POST("/settings/integrations/telegram/token", h.telegramToken)
	r.Admin.POST("/settings/integrations/telegram/detect", h.telegramDetect)
	r.Admin.POST("/settings/integrations/telegram/chat", h.telegramChat)
	r.Admin.POST("/settings/integrations/telegram/test", h.telegramTest)
	r.Admin.GET("/settings/ssh", h.sshPage)
	r.Admin.POST("/settings/ssh/regenerate", h.sshRegenerate)
	r.Admin.POST("/settings/ssh/personal-keys", h.sshPersonalKeys)
	r.Admin.POST("/settings/ssh/hosts/:id/accept", h.sshAccept)
	r.Admin.POST("/settings/ssh/hosts/:id/forget", h.sshForget)
	r.Admin.GET("/settings/backups", h.backupsPage)
	r.Admin.POST("/settings/backups", h.backupsSave)
	r.Admin.POST("/settings/backups/run", h.backupsRun)
	r.Admin.GET("/settings/backups/latest", h.backupsLatest)
	r.Admin.GET("/settings/notifications", h.notificationsPage)
	r.Admin.POST("/settings/notifications/:type", h.notificationsSet)
	r.Admin.POST("/notifications/:id/retry", h.notificationRetry)

	r.Admin.GET("/settings", func(c *echo.Context) error { return web.Redirect(c, "/settings/general") })
	r.Admin.GET("/settings/general", h.generalPage)
	r.Admin.POST("/settings/general", h.generalSave)
	r.Admin.GET("/settings/security", h.securityPage)
	r.Admin.POST("/settings/security/password", h.changePassword)
	r.Admin.POST("/settings/security/lockout", h.saveLockout)
	r.Admin.POST("/settings/security/sessions/:id/sign-out", h.signOutSession)
	r.Admin.POST("/settings/security/sessions/sign-out-everywhere", h.signOutEverywhere)
	return h.shell
}

var groupOrder = []string{"overview", "servers", "subscriptions", "routing", "routerscripts", "system"}

// shell builds the layout's data for the current request.
func (h *handler) shell(c *echo.Context, title, path string) ui.Shell {
	q := web.FromContext(c.Request().Context())
	s := ui.Shell{
		Title: title, Path: path, Lang: q.Loc.Lang,
		Nav: h.navGroups(), Keys: h.keymap(), JobsCell: h.jobsCellView(c), PageKeys: "ui.hints.default",
	}
	if q.Session != nil {
		s.CSRF = q.Session.CSRF
	}
	if bad, err := h.Notify.LatestFailed(c.Request().Context()); err != nil {
		h.Log.Error("pages: notification health", "error", err)
	} else if bad {
		s.Warning = &ui.HeaderWarning{Text: i18n.T(c.Request().Context(), "ui.warn.telegram_failing"), Href: "/#failed-notifications"}
	}
	if q.Admin != nil {
		s.Admin = q.Admin.Username
	}
	return s
}

func (h *handler) navGroups() []ui.NavGroup {
	by := map[string][]ui.NavItem{}
	for _, it := range h.Nav {
		by[it.Group] = append(by[it.Group], it)
	}
	var out []ui.NavGroup
	add := func(name string) {
		items := by[name]
		if len(items) == 0 {
			return
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
		out = append(out, ui.NavGroup{Label: "nav.group." + name, Items: items})
		delete(by, name)
	}
	for _, g := range groupOrder {
		add(g)
	}
	rest := make([]string, 0, len(by))
	for g := range by {
		rest = append(rest, g)
	}
	sort.Strings(rest)
	for _, g := range rest {
		add(g)
	}
	return out
}

// keymap is the ? sheet. The g letters come from the nav, so only pages that
// exist are listed.
func (h *handler) keymap() ui.Keymap {
	anywhere := ui.KeyGroup{Where: "keys.anywhere", Keys: []ui.Key{
		{Keys: "/", What: "keys.search"}, {Keys: ":", What: "keys.actions"}, {Keys: "?", What: "keys.keys"}, {Keys: "esc", What: "keys.esc"},
	}}
	goTo := ui.KeyGroup{Where: "keys.goto"}
	for _, g := range h.navGroups() {
		for _, it := range g.Items {
			if it.GoKey != "" {
				goTo.Keys = append(goTo.Keys, ui.Key{Keys: "g " + it.GoKey, What: it.Label})
			}
		}
	}
	return ui.Keymap{anywhere, goTo,
		{Where: "keys.lists", Keys: []ui.Key{{Keys: "j k", What: "keys.move"}, {Keys: "↵", What: "keys.open"}}},
		{Where: "keys.settings", Keys: []ui.Key{{Keys: "[ ]", What: "keys.sections"}, {Keys: "⌘↵", What: "keys.save"}}},
	}
}

func (h *handler) cookieSecure() bool { return h.secure }

// Package pages holds the platform's own admin pages: sign-in, dashboard,
// settings, search. Other modules add theirs through module.RouteDeclarer.
package pages

import (
	"context"
	"log/slog"
	"sort"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/backup"
	"github.com/tikhonp/proxier/internal/platform/bot"
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
	Search(ctx context.Context, w access.Who, q string, limit int) ([]ui.SearchHit, error)
}

// Integrator is a module with rows on Settings → Integrations.
type Integrator interface {
	Integrations(ctx context.Context) []ui.IntegrationRow
}

// SubjectOwner is module.SubjectOwner: the subjects a member owns.
type SubjectOwner interface {
	OwnedSubjects(ctx context.Context, member int64) ([]events.Subject, error)
}

// Dashboarder is a module with areas on the dashboard.
type Dashboarder interface {
	Dashboard(ctx context.Context, w access.Who) ([]ui.DashboardArea, error)
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
	Bot        *bot.Service
	SSH        *sshx.SSH
	Tailnet    *tailnet.Node
	Backup     *backup.Service
	Namers     []Namer
	// Owners list what each member owns, for Activity and Jobs at
	// activity: own.
	Owners []SubjectOwner
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
	r.Open.POST("/login/code", h.loginCode)
	r.Open.POST("/login/passkey/options", h.loginPasskeyOptions)
	r.Open.POST("/login/passkey", h.loginPasskey)
	r.Open.GET("/setup/:token", h.setupPage)
	r.Open.POST("/setup/:token", h.setupPost)

	// Every member's, whatever their grant: their own sign-in and language,
	// and pages that show only what the grant reaches.
	me := r.Member
	me.POST("/logout", h.logout)
	me.GET("/", h.dashboard)
	me.GET("/search", h.search)
	me.GET("/settings", func(c *echo.Context) error { return web.Redirect(c, "/settings/general") })
	me.GET("/settings/general", h.generalPage)
	me.POST("/settings/general/language", h.setLanguage)
	me.GET("/settings/security", h.securityPage)
	me.POST("/settings/security/password", h.changePassword)
	me.POST("/settings/security/totp/setup", h.totpSetup)
	me.POST("/settings/security/totp/enable", h.totpEnable)
	me.POST("/settings/security/totp/disable", h.totpDisable)
	me.POST("/settings/security/passkeys/options", h.passkeyOptions)
	me.POST("/settings/security/passkeys", h.passkeyAdd)
	me.POST("/settings/security/passkeys/:id/remove", h.passkeyRemove)
	me.POST("/settings/security/sessions/:id/sign-out", h.signOutSession)
	me.POST("/settings/security/sessions/sign-out-everywhere", h.signOutEverywhere)
	me.GET("/settings/notifications", h.notificationsPage)
	me.POST("/settings/notifications/:type", h.notificationsSet)
	me.POST("/settings/notifications/telegram/detect", h.notificationsDetect)
	me.POST("/settings/notifications/telegram/chat", h.notificationsChat)
	me.POST("/settings/notifications/telegram/test", h.notificationsTest)
	me.POST("/notifications/:id/retry", h.notificationRetry)

	// Cancel and Retry also need the job's own area (jobManage).
	activity := r.Area(access.Activity).View
	activity.GET("/jobs", h.jobsPage)
	activity.GET("/jobs/cell", h.jobsCell)
	activity.GET("/jobs/:id", h.jobPage)
	activity.GET("/jobs/:id/stream", h.jobStream)
	activity.GET("/jobs/:id/log.txt", h.jobLogText)
	activity.POST("/jobs/:id/cancel", h.jobCancel)
	activity.POST("/jobs/:id/retry", h.jobRetry)
	activity.GET("/activity", h.activityPage)

	settings := r.Area(access.Settings).Manage
	settings.POST("/jobs/demo", h.runDemo)
	settings.POST("/settings/general", h.generalSave)
	settings.POST("/settings/security/lockout", h.saveLockout)
	settings.GET("/settings/members", h.membersPage)
	settings.GET("/settings/members/new", h.memberNewPage)
	settings.POST("/settings/members", h.memberAdd)
	settings.GET("/settings/members/:id", h.memberPage)
	settings.POST("/settings/members/:id/access", h.memberAccess)
	settings.POST("/settings/members/:id/setup-link", h.memberSetupLink)
	settings.POST("/settings/members/:id/remove", h.memberRemove)
	settings.GET("/settings/integrations", h.integrationsPage)
	settings.GET("/settings/integrations/tailnet", h.tailnetPage)
	settings.POST("/settings/integrations/tailnet/reauth", h.tailnetReauth)
	settings.GET("/settings/integrations/telegram", h.telegramPage)
	settings.POST("/settings/integrations/telegram/token", h.telegramToken)
	settings.GET("/settings/ssh", h.sshPage)
	settings.POST("/settings/ssh/regenerate", h.sshRegenerate)
	settings.POST("/settings/ssh/personal-keys", h.sshPersonalKeys)
	settings.POST("/settings/ssh/hosts/:id/accept", h.sshAccept)
	settings.POST("/settings/ssh/hosts/:id/forget", h.sshForget)
	settings.GET("/settings/backups", h.backupsPage)
	settings.POST("/settings/backups", h.backupsSave)
	settings.POST("/settings/backups/run", h.backupsRun)
	settings.GET("/settings/backups/latest", h.backupsLatest)
	return h.shell
}

var groupOrder = []string{"overview", "servers", "subscriptions", "routing", "routerscripts", "system"}

// shell builds the layout's data for the current request.
func (h *handler) shell(c *echo.Context, title, path string) ui.Shell {
	q := web.FromContext(c.Request().Context())
	s := ui.Shell{
		Title: title, Path: path, Lang: q.Loc.Lang, Grant: q.Grant,
		Nav: h.navGroups(q.Grant), Keys: h.keymap(q.Grant), PageKeys: "ui.hints.default",
	}
	if q.Grant.Sees(access.Activity) {
		s.JobsCell = h.jobsCellView(c)
	}
	if q.Session != nil {
		s.CSRF = q.Session.CSRF
	}
	// Each member is warned of their own notifications failing.
	if q.Member != nil {
		if bad, err := h.Notify.LatestFailed(c.Request().Context(), q.Member.ID); err != nil {
			h.Log.Error("pages: notification health", "error", err)
		} else if bad {
			s.Warning = &ui.HeaderWarning{Text: i18n.T(c.Request().Context(), "ui.warn.telegram_failing"), Href: "/#failed-notifications"}
		}
	}
	if q.Member != nil {
		s.Member = q.Member.Username
	}
	return s
}

// navGroups is the sidebar of a member with grant g: only what they see.
func (h *handler) navGroups(g access.Grant) []ui.NavGroup {
	by := map[string][]ui.NavItem{}
	for _, it := range h.Nav {
		if g.Reaches(it.Area, it.Level) {
			by[it.Group] = append(by[it.Group], it)
		}
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
// exist and the member sees are listed.
func (h *handler) keymap(g access.Grant) ui.Keymap {
	anywhere := ui.KeyGroup{Where: "keys.anywhere", Keys: []ui.Key{
		{Keys: "/", What: "keys.search"}, {Keys: ":", What: "keys.actions"}, {Keys: "?", What: "keys.keys"}, {Keys: "esc", What: "keys.esc"},
	}}
	goTo := ui.KeyGroup{Where: "keys.goto"}
	for _, ng := range h.navGroups(g) {
		for _, it := range ng.Items {
			if it.GoKey != "" {
				goTo.Keys = append(goTo.Keys, ui.Key{Keys: "g " + it.GoKey, What: it.Label})
			}
		}
	}
	return ui.Keymap{anywhere, goTo,
		{Where: "keys.lists", Keys: []ui.Key{{Keys: "j k", What: "keys.move"}, {Keys: "⌃N ⌃P", What: "keys.move_ctrl"}, {Keys: "↵", What: "keys.open"}}},
		{Where: "keys.settings", Keys: []ui.Key{{Keys: "[ ]", What: "keys.sections"}, {Keys: "⌘↵", What: "keys.save"}}},
		{Where: "keys.log", Keys: []ui.Key{{Keys: "f", What: "keys.follow"}, {Keys: "G", What: "keys.log_end"}}},
	}
}

func (h *handler) cookieSecure() bool { return h.secure }

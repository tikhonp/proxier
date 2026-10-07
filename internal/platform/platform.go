// Package platform assembles everything the modules stand on
// (docs/modules/platform.md): configuration, the database, the vault, the
// event catalog, settings and the HTTP listener. The platform is itself a
// module named "platform": it owns the unprefixed tables and the platform
// events.
package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"slices"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/backup"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/httpx"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/migrations"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/pages"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/vault"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Name is the platform's module name.
const Name = "platform"

// App is the assembled platform with its modules.
type App struct {
	Cfg      *config.Config
	Log      *slog.Logger
	DB       *db.DB
	Vault    *vault.Vault
	Events   *events.Catalog
	Settings *settings.Store
	I18n     *i18n.Catalog
	Auth     *auth.Service
	Jobs     *jobs.System
	// Dispatcher delivers committed events to subscribers.
	Dispatcher *events.Dispatcher
	// Notify renders and queues notifications; Telegram is its channel.
	Notify   *notify.Service
	Telegram *telegram.Channel
	// SSH is the SSH client with Proxier's key and the pinned host keys;
	// Tailnet is the node it dials routers through; Backup writes the nightly
	// database snapshots.
	SSH     *sshx.SSH
	Tailnet *tailnet.Node
	Backup  *backup.Service
	// closing is closed when Serve starts shutting down, so live streams end
	// before the HTTP server's grace period runs out.
	closing chan struct{}
	// Modules are the platform followed by the registered modules, in the
	// order their migrations run.
	Modules []module.Module
}

// Open builds the platform for modules: it opens the database (creating the
// data directory), declares every module's event types and registers every
// settings section. It does not migrate; call Migrate before serving.
func Open(cfg *config.Config, log *slog.Logger, modules ...module.Module) (*App, error) {
	all := append([]module.Module{core{cfg}}, modules...)
	if err := module.Validate(all); err != nil {
		return nil, err
	}
	v, err := vault.New(cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	d, err := db.Open(cfg.DatabasePath())
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Log: log, DB: d, Vault: v, Events: events.NewCatalog(), Modules: all}
	a.Settings = settings.New(d, v, a.Events)
	a.I18n = i18n.NewCatalog()
	a.Auth = auth.New(d, v, a.Events, a.Settings)
	a.Auth.Log = log
	a.Jobs = jobs.New(d, v, a.Events, a.Settings, log)
	a.Dispatcher = events.NewDispatcher(d, log)
	a.Notify = notify.New(d, a.Events, a.I18n, a.Jobs, a.Auth, a.Settings, cfg.BaseURL, log)
	a.Telegram = telegram.NewChannel(a.Settings, nil, "")
	a.Tailnet = tailnet.New(cfg, log)
	a.SSH = sshx.New(d, v, a.Events, a.Settings, a.Tailnet.Dial, log)
	a.Backup = backup.New(d, cfg, a.Settings, a.Events, log)
	a.closing = make(chan struct{})

	var errs []error
	for _, m := range all {
		if ed, ok := m.(module.EventDeclarer); ok {
			errs = append(errs, a.Events.Declare(ed.EventTypes()...))
		}
		if sd, ok := m.(module.SettingsDeclarer); ok {
			errs = append(errs, a.Settings.Register(sd.SettingsSections()...))
		}
		if md, ok := m.(module.MessagesDeclarer); ok {
			errs = append(errs, a.I18n.Add(m.Name(), md.Messages()))
		}
	}
	if err := errors.Join(errs...); err != nil {
		_ = d.Close()
		return nil, err
	}
	deps := module.Deps{Cfg: cfg, Log: log, DB: d, Vault: v, Events: a.Events, Settings: a.Settings, I18n: a.I18n, Auth: a.Auth,
		Jobs: a.Jobs, Dispatcher: a.Dispatcher, Notify: a.Notify, SSH: a.SSH, Tailnet: a.Tailnet}
	for _, m := range all {
		if in, ok := m.(module.Initializer); ok {
			if err := in.Init(deps); err != nil {
				_ = d.Close()
				return nil, fmt.Errorf("init %s: %w", m.Name(), err)
			}
		}
	}
	// What a module declares as code (job types, subscribers, renderers) is
	// asked for after Init: the services it builds there are what the
	// declarations point at.
	for _, m := range all {
		if jd, ok := m.(module.JobDeclarer); ok {
			errs = append(errs, a.Jobs.Register(m.Name(), jd.JobTypes()...), a.Jobs.RegisterSchedules(m.Name(), jd.Schedules()...))
		}
		if sd, ok := m.(module.SubscriberDeclarer); ok {
			for _, sub := range sd.Subscribers() {
				errs = append(errs, a.Dispatcher.Subscribe(sub))
			}
		}
		if nr, ok := m.(module.NotificationRenderer); ok {
			errs = append(errs, a.Notify.AddRenderer(m.Name(), nr))
		}
		if n, ok := m.(module.SubjectNamer); ok {
			errs = append(errs, a.Notify.AddNamer(n))
		}
	}
	errs = append(errs, a.Notify.AddChannel(a.Telegram), a.Jobs.Register(Name, a.Notify.JobType()),
		a.Dispatcher.Subscribe(a.Notify.Subscriber()),
		a.Jobs.Register(Name, a.Backup.JobType()), a.Jobs.RegisterSchedules(Name, a.Backup.Schedule()))
	types, schedules := a.Jobs.PlatformTypes()
	errs = append(errs, a.Jobs.Register(Name, types...), a.Jobs.RegisterSchedules(Name, schedules...))
	if err := errors.Join(errs...); err != nil {
		_ = d.Close()
		return nil, err
	}
	return a, nil
}

// Module returns a registered module by name.
func (a *App) Module(name string) (module.Module, bool) {
	for _, m := range a.Modules {
		if m.Name() == name {
			return m, true
		}
	}
	return nil, false
}

// Migrate applies every module's pending migrations, the platform's first,
// then makes sure Proxier has its SSH key (generated on the first start and
// kept) and runs the modules' AfterMigrate hooks in module order.
func (a *App) Migrate(ctx context.Context) error {
	if err := db.MigrateUp(ctx, a.DB, a.Log, a.migrationSources()...); err != nil {
		return err
	}
	name, err := a.Settings.Get(ctx, "general.instance_name")
	if err != nil {
		return err
	}
	if err := a.SSH.EnsureIdentity(ctx, name); err != nil {
		return err
	}
	for _, m := range a.Modules {
		if mg, ok := m.(module.Migrated); ok {
			if err := mg.AfterMigrate(ctx); err != nil {
				return fmt.Errorf("after migrate %s: %w", m.Name(), err)
			}
		}
	}
	return nil
}

func (a *App) migrationSources() []db.MigrationSource {
	out := make([]db.MigrationSource, len(a.Modules))
	for i, m := range a.Modules {
		out[i] = m
	}
	return out
}

// MigrationStatus lists every module's migrations.
func (a *App) MigrationStatus(ctx context.Context) ([]db.MigrationStatus, error) {
	return db.Status(ctx, a.DB, a.migrationSources()...)
}

// Health is /healthz: the database answers, every worker pool, the scheduler
// and every event subscriber keep looping, and no job lease has expired.
func (a *App) Health(ctx context.Context) error {
	return errors.Join(a.DB.Ping(ctx), a.Jobs.Health(), a.Dispatcher.Health())
}

// HTTP builds the HTTP app: the platform's pages, then every module's routes.
func (a *App) HTTP() *echo.Echo {
	e := httpx.New(httpx.Options{Log: a.Log, TrustedProxies: a.Cfg.TrustedProxies, Health: a.Health})
	r := web.Mount(e, web.Deps{Log: a.Log, Auth: a.Auth, I18n: a.I18n, Settings: a.Settings, BaseURL: a.Cfg.BaseURL},
		ui.StaticFS, ui.StaticHash)

	pd := pages.Deps{Log: a.Log, Cfg: a.Cfg, Auth: a.Auth, Settings: a.Settings, I18n: a.I18n,
		Jobs: a.Jobs, Events: a.Events, Notify: a.Notify, Telegram: a.Telegram, Query: a.DB.R, Closing: a.closing,
		SSH: a.SSH, Tailnet: a.Tailnet, Backup: a.Backup}
	for _, m := range a.Modules {
		if nd, ok := m.(module.NavDeclarer); ok {
			pd.Nav = append(pd.Nav, nd.Nav()...)
		}
		if sp, ok := m.(module.SettingsPageDeclarer); ok {
			pd.SettingsPages = append(pd.SettingsPages, sp.SettingsPages()...)
		}
		if ig, ok := m.(module.IntegrationDeclarer); ok {
			pd.Integrators = append(pd.Integrators, ig)
		}
		if dd, ok := m.(module.DashboardDeclarer); ok {
			pd.Dashboards = append(pd.Dashboards, dd)
		}
		if s, ok := m.(module.Searcher); ok {
			pd.Searchers = append(pd.Searchers, s)
		}
		if n, ok := m.(module.SubjectNamer); ok {
			pd.Namers = append(pd.Namers, n)
		}
	}
	r.Shell = pages.Register(r, pd)
	r.SettingsPages = func() []ui.SettingsPage { return pd.SettingsPages }
	for _, m := range a.Modules {
		if rd, ok := m.(module.RouteDeclarer); ok {
			rd.Routes(r)
		}
	}
	return e
}

// Serve runs the HTTP listener, the job workers, the scheduler and the event
// dispatcher until ctx ends, then shuts down in order: stop HTTP intake, stop
// the scheduler and claiming, give running steps their grace, mark what still
// runs interrupted, stop the dispatcher. The caller closes the database.
func (a *App) Serve(ctx context.Context, onListen func(net.Addr)) error {
	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	dispCtx, stopDisp := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	defer stopDisp()
	jobsDone, dispDone := make(chan error, 1), make(chan error, 1)
	// The tailnet node joins in the background: HTTP does not wait for it.
	a.Tailnet.Start(ctx)
	go func() { jobsDone <- a.Jobs.Start(jobsCtx) }()
	go func() { dispDone <- a.Dispatcher.Start(dispCtx) }()

	go func() { <-ctx.Done(); close(a.closing) }()
	httpErr := httpx.Run(ctx, a.HTTP(), a.Cfg.Listen, onListen)

	stopJobs()
	jobsErr := <-jobsDone
	stopDisp()
	dispErr := <-dispDone
	// After the jobs: a running step may still be dialling through it.
	return errors.Join(httpErr, jobsErr, dispErr, a.Tailnet.Close())
}

// Close closes the database.
func (a *App) Close() error { return a.DB.Close() }

// core is the platform as a module.
type core struct{ cfg *config.Config }

func (core) Name() string      { return Name }
func (core) Migrations() fs.FS { return migrations.FS }

func (core) EventTypes() []events.Type { return Events }

// Events is the platform's event catalog (docs/events.md#platform): the
// sign-in events belong to auth, the rest to the platform's other parts.
var Events = slices.Concat(auth.Events, sshx.Events, backup.Events, []events.Type{
	settings.ChangedEvent,
	jobs.ScheduleEnabledChanged,
	notify.FailedEvent,
	{Name: "job.failed", Module: Name, Notify: true, Emoji: "🔴", Description: "A job failed (job types without their own failure event)."},
})

func (core) Messages() i18n.Messages { return messages }

func (core) Nav() []ui.NavItem {
	return []ui.NavItem{
		{Group: "overview", Label: "nav.dashboard", Href: "/", GoKey: "d", Order: 10},
		{Group: "system", Label: "nav.jobs", Href: "/jobs", GoKey: "j", Order: 100},
		{Group: "system", Label: "nav.activity", Href: "/activity", GoKey: "a", Order: 110},
		{Group: "system", Label: "nav.settings", Href: "/settings", GoKey: ",", Order: 900},
	}
}

func (core) SettingsPages() []ui.SettingsPage {
	return []ui.SettingsPage{
		{Slug: "general", Title: "settings.general", Order: 10},
		{Slug: "security", Title: "settings.security", Order: 20},
		{Slug: "integrations", Title: "settings.integrations", Order: 40},
		{Slug: "notifications", Title: "settings.notifications", Order: 50},
	}
}

func (c core) SettingsSections() []settings.Section {
	return []settings.Section{auth.SecuritySection, telegram.Section, sshx.Section, backup.Section, {
		Name: "general", Module: Name,
		Fields: []settings.Field{
			{Key: "general.instance_name", Kind: settings.String, Default: "Proxier", MaxLen: 64},
			{Key: "general.time_zone", Kind: settings.String, Default: c.cfg.TZ.String(), MaxLen: 64, Validate: validTimeZone},
			{Key: "general.language", Kind: settings.Enum, Default: "en", Options: []string{"en", "ru"}},
			// Shown in stub entries, e.g. "@tikhonp".
			{Key: "general.admin_contact", Kind: settings.String, MaxLen: 64},
		},
	}}
}

// RenderNotification writes the texts the default cannot: the browser behind a
// sign-in comes from its user agent.
func (core) RenderNotification(_ context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error) {
	if e.Type != auth.SignedInEvent {
		return notify.Message{}, false, nil
	}
	ip, _ := e.Payload["ip"].(string)
	ua, _ := e.Payload["user_agent"].(string)
	return notify.Message{
		Title: loc.T("notify.auth.signed_in", i18n.Args{"ip": ip}),
		Body:  loc.T("notify.auth.signed_in.body", i18n.Args{"browser": auth.BrowserName(ua)}),
	}, true, nil
}

// SubjectTypes are the subject types the platform's SSH events use; the
// other platform subjects are named by the pages and the notifier themselves.
func (core) SubjectTypes() []string { return sshx.SubjectTypes }

// NameSubjects names the SSH identity and the pinned hosts (whose id is their
// address, so no lookup is needed).
func (core) NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error) {
	out := map[string]ui.SubjectRef{}
	for _, id := range ids {
		switch typ {
		case "ssh":
			out[id] = ui.SubjectRef{Label: i18n.T(ctx, "subject.ssh"), Href: "/settings/ssh"}
		case "ssh_host":
			out[id] = ui.SubjectRef{Label: id, Href: "/settings/ssh#hosts"}
		}
	}
	return out, nil
}

func validTimeZone(s string) error {
	if s == "" {
		return errors.New("required")
	}
	if _, err := time.LoadLocation(s); err != nil {
		return errors.New("unknown time zone")
	}
	return nil
}

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
	"os"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/httpx"
	"github.com/tikhonp/proxier/internal/platform/migrations"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
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

	var errs []error
	for _, m := range all {
		if ed, ok := m.(module.EventDeclarer); ok {
			errs = append(errs, a.Events.Declare(ed.EventTypes()...))
		}
		if sd, ok := m.(module.SettingsDeclarer); ok {
			errs = append(errs, a.Settings.Register(sd.SettingsSections()...))
		}
	}
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

// Migrate applies every module's pending migrations, the platform's first.
func (a *App) Migrate(ctx context.Context) error {
	return db.MigrateUp(ctx, a.DB, a.Log, a.migrationSources()...)
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

// Health is /healthz: the database answers. The job workers join it later.
func (a *App) Health(ctx context.Context) error {
	return a.DB.Ping(ctx)
}

// HTTP builds the HTTP app.
func (a *App) HTTP() *echo.Echo {
	return httpx.New(httpx.Options{Log: a.Log, TrustedProxies: a.Cfg.TrustedProxies, Health: a.Health})
}

// Close closes the database.
func (a *App) Close() error { return a.DB.Close() }

// core is the platform as a module.
type core struct{ cfg *config.Config }

func (core) Name() string      { return Name }
func (core) Migrations() fs.FS { return migrations.FS }

func (core) EventTypes() []events.Type { return Events }

// Events is the platform's event catalog (docs/events.md#platform).
var Events = []events.Type{
	{Name: "admin.created", Module: Name, Description: "The admin was created from the command line."},
	// Notifies only when the payload has new_ip: true (the notifier filters).
	{Name: "auth.signed_in", Module: Name, Notify: true, Description: "Signed in from a new IP."},
	{Name: "auth.sign_in_failed", Module: Name, Description: "A sign-in failed."},
	{Name: "auth.locked", Module: Name, Notify: true, Description: "An IP was locked out after failed sign-ins."},
	{Name: "auth.password_changed", Module: Name, Notify: true, Description: "The admin password was changed."},
	{Name: "auth.signed_out_everywhere", Module: Name, Description: "Every session was ended."},
	settings.ChangedEvent,
	{Name: "ssh.host_key_changed", Module: Name, Notify: true, Description: "A pinned SSH host key changed; work with that host stops."},
	{Name: "ssh.host_key_accepted", Module: Name, Description: "A new SSH host key was accepted."},
	{Name: "job.failed", Module: Name, Notify: true, Description: "A job failed (job types without their own failure event)."},
	{Name: "backup.completed", Module: Name, Description: "A database backup was written."},
	{Name: "backup.failed", Module: Name, Notify: true, Description: "A database backup failed."},
}

func (c core) SettingsSections() []settings.Section {
	return []settings.Section{{
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

func validTimeZone(s string) error {
	if s == "" {
		return errors.New("required")
	}
	if _, err := time.LoadLocation(s); err != nil {
		return errors.New("unknown time zone")
	}
	return nil
}

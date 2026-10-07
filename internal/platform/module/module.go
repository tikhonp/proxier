// Package module defines what a Proxier module is (ADR 0002). A module is a Go
// package compiled into the binary and enabled by being passed to
// platform.New in main. It owns its tables, migrations, routes, jobs, events,
// settings and translations, and talks to other modules only through ports
// and events.
//
// Module is the minimum every module implements. A module declares the rest
// by also implementing the optional interfaces next to it; the platform
// discovers them with a type assertion, so a module implements only what it
// has.
package module

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"

	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/vault"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Module is a registered part of Proxier.
type Module interface {
	// Name is short and lowercase ("platform", "servers"). It names the
	// module's migration version table and prefixes its events.
	Name() string
	// Migrations holds the module's goose *.sql files at its root, or is nil
	// when the module has no tables.
	Migrations() fs.FS
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,23}$`)

// Validate checks that modules have valid, unique names.
func Validate(modules []Module) error {
	seen := map[string]bool{}
	var errs []error
	for _, m := range modules {
		n := m.Name()
		if !namePattern.MatchString(n) {
			errs = append(errs, fmt.Errorf("module name %q must match %s", n, namePattern))
		}
		if seen[n] {
			errs = append(errs, fmt.Errorf("module %q is registered twice", n))
		}
		seen[n] = true
	}
	return errors.Join(errs...)
}

// EventDeclarer is a module that records events. Its types join the catalog
// at startup, with their default notification rule.
type EventDeclarer interface {
	EventTypes() []events.Type
}

// SettingsDeclarer is a module with sections in Settings.
type SettingsDeclarer interface {
	SettingsSections() []settings.Section
}

// Deps is what a module may use of the platform. It grows with the
// sub-phases (jobs, notifications, SSH, tailnet).
type Deps struct {
	Cfg      *config.Config
	Log      *slog.Logger
	DB       *db.DB
	Vault    *vault.Vault
	Events   *events.Catalog
	Settings *settings.Store
	I18n     *i18n.Catalog
	Auth     *auth.Service
}

// Initializer is called by platform.Open after the platform services exist.
type Initializer interface{ Init(d Deps) error }

// MessagesDeclarer is a module with translations. Keys belong to the module.
type MessagesDeclarer interface{ Messages() i18n.Messages }

// RouteDeclarer is a module with pages or endpoints.
type RouteDeclarer interface{ Routes(r web.Routes) }

// NavDeclarer is a module with sidebar entries.
type NavDeclarer interface{ Nav() []ui.NavItem }

// SettingsPageDeclarer is a module with its own pages in Settings.
type SettingsPageDeclarer interface{ SettingsPages() []ui.SettingsPage }

// Searcher is a module whose things appear in the search pop-up's "Go to".
type Searcher interface {
	Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error)
}

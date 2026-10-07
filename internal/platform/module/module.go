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
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
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
	// Jobs enqueues work; Dispatcher is for modules that react to events
	// outside SubscriberDeclarer.
	Jobs       *jobs.System
	Dispatcher *events.Dispatcher
	// Notify is for modules that read or change notification rules.
	Notify *notify.Service
	// SSH connects to servers, jump hosts and routers; Tailnet is the node it
	// dials routers through.
	SSH     *sshx.SSH
	Tailnet *tailnet.Node
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

// JobDeclarer is a module with job types and schedules (docs/processes/platform/jobs.md).
type JobDeclarer interface {
	JobTypes() []jobs.Type
	Schedules() []jobs.Schedule
}

// SubscriberDeclarer is a module that reacts to committed events.
type SubscriberDeclarer interface{ Subscribers() []events.Subscriber }

// SubjectNamer names the subjects of events and jobs for Activity and the Jobs
// pages: a label and a link. An unnamed subject shows as type:id.
type SubjectNamer interface {
	SubjectTypes() []string
	NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error)
}

// NotificationRenderer writes the notification of the module's own event
// types where the default text (notify.<type> with the payload's fields) is
// not enough. ok=false leaves the event to the default. Title and Body are
// enough: the emoji, the link and the button are added when left empty.
type NotificationRenderer interface {
	RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error)
}

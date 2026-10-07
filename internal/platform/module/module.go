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
	"errors"
	"fmt"
	"io/fs"
	"regexp"

	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
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

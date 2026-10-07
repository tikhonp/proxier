// Package servers is the module that builds VPS proxy servers from versioned
// templates and watches whether they work from Russia
// (docs/modules/servers.md). This file is wiring only: every statement of SQL
// is in store, the template lifecycle in templates, the pure checks in
// manifest, render and validate.
package servers

import (
	"context"
	"io/fs"
	"strconv"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/migrations"
	"github.com/tikhonp/proxier/internal/modules/servers/pages"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// seedMarker is the servers_meta key that records the seed template was made.
const seedMarker = "seed.vless-xhttp"

// Module is the servers module.
type Module struct {
	deps      module.Deps
	Store     *store.Store
	Templates *templates.Service
}

// New returns the module; Init gives it the platform's services.
func New() *Module { return &Module{} }

func (*Module) Name() string      { return modName }
func (*Module) Migrations() fs.FS { return migrations.FS }

// Init builds the module's services.
func (m *Module) Init(d module.Deps) error {
	m.deps = d
	m.Store = store.New(d.DB, d.Events)
	m.Templates = templates.New(d.DB, d.Events, d.Log)
	return nil
}

// AfterMigrate creates the seed template on the first start, once: a marker
// keeps it from coming back after the admin deletes it.
func (m *Module) AfterMigrate(ctx context.Context) error {
	created, err := m.Templates.Seed(ctx, templates.SeedSpec{
		Slug: seed.Slug, Name: seed.Name, Description: seed.Description, Notes: seed.Notes,
		Files: seed.Files(), MarkerKey: seedMarker,
	}, events.ActorSystem)
	if err != nil {
		return err
	}
	if created {
		m.deps.Log.Info("servers: created the seed template", "slug", seed.Slug)
	}
	return nil
}

func (*Module) EventTypes() []events.Type { return Events }

func (*Module) SettingsSections() []settings.Section { return []settings.Section{Section} }

func (*Module) Messages() i18n.Messages { return messages }

func (m *Module) Routes(r web.Routes) {
	pages.Register(r, pages.Deps{Store: m.Store, Log: m.deps.Log})
}

// Nav adds the module's entries; each sub-phase adds its own with its page.
func (*Module) Nav() []ui.NavItem {
	return []ui.NavItem{
		{Group: "servers", Label: "locations.nav", Href: "/locations", Order: 30},
	}
}

// Search finds locations by code or name.
func (m *Module) Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error) {
	locs, err := m.Store.Locations(ctx)
	if err != nil {
		return nil, err
	}
	lang := i18n.From(ctx).Lang
	q = strings.ToLower(strings.TrimSpace(q))
	var out []ui.SearchHit
	for _, l := range locs {
		if q != "" && !strings.Contains(l.Code, q) && !strings.Contains(strings.ToLower(l.Name), q) &&
			!strings.Contains(strings.ToLower(country.Name(l.Country, string(lang))), q) {
			continue
		}
		out = append(out, ui.SearchHit{Label: country.Flag(l.Country) + " " + l.Code + " · " + l.Name,
			Meta: i18n.T(ctx, "locations.search"), Href: "/locations"})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// SubjectTypes are the subject types of the module's events.
func (*Module) SubjectTypes() []string { return []string{"server", "template", "location", "home"} }

// NameSubjects names subjects for Activity, Jobs and notifications.
func (m *Module) NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error) {
	out := map[string]ui.SubjectRef{}
	for _, id := range ids {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		switch typ {
		case "home":
			out[id] = ui.SubjectRef{Label: i18n.T(ctx, "subject.home"), Href: "/"}
		case "location":
			var l struct{ Code, Name, Country string }
			if err := m.deps.DB.R.GetContext(ctx, &l, `SELECT code, name, country FROM servers_locations WHERE id = ?`, n); err == nil {
				out[id] = ui.SubjectRef{Label: l.Code + " · " + l.Name, Href: "/locations"}
			}
		case "template":
			var name string
			if err := m.deps.DB.R.GetContext(ctx, &name, `SELECT name FROM servers_templates WHERE id = ?`, n); err == nil {
				out[id] = ui.SubjectRef{Label: name, Href: "/templates/" + id}
			}
		case "server":
			var s struct{ Name, Country string }
			if err := m.deps.DB.R.GetContext(ctx, &s, `SELECT s.name, l.country FROM servers_servers s JOIN servers_locations l ON l.id = s.location_id WHERE s.id = ?`, n); err == nil {
				out[id] = ui.SubjectRef{Label: country.Flag(s.Country) + " " + s.Name, Href: "/servers/" + id}
			}
		}
	}
	return out, nil
}

var (
	_ module.Module           = (*Module)(nil)
	_ module.Initializer      = (*Module)(nil)
	_ module.Migrated         = (*Module)(nil)
	_ module.EventDeclarer    = (*Module)(nil)
	_ module.SettingsDeclarer = (*Module)(nil)
	_ module.MessagesDeclarer = (*Module)(nil)
	_ module.RouteDeclarer    = (*Module)(nil)
	_ module.NavDeclarer      = (*Module)(nil)
	_ module.SubjectNamer     = (*Module)(nil)
	_ module.Searcher         = (*Module)(nil)
)

// Package routing is the module that decides which domains go through the
// VPN and keeps routers and the Shadowrocket config in sync with it
// (docs/modules/routing.md). This file is wiring only. The module talks to
// servers only through its ports (ADR 0002).
package routing

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/migrations"
	"github.com/tikhonp/proxier/internal/modules/routing/pages"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Ports are what the module uses of the servers module. A nil one hides its features.
type Ports struct {
	Hostnames servers.ServerHostnames // the guard (3b)
	Catalog   servers.EndpointCatalog // discovery's server picker (3g)
	Dialer    servers.ProxyDialer     // discovery through a server (3g)
}

// Module is the routing module.
type Module struct {
	// Now is the module's clock; every service reads it, so a test replaces it once.
	Now func() time.Time
	// Endpoints are the upstream base URLs; tests replace them before Open.
	Endpoints sources.Endpoints
	// Marker marks targets for sync: change.None until router sync (3e).
	// Services hold the module itself, which forwards to it at call time.
	Marker   change.Marker
	Services *services.Service
	Lists    *lists.Service

	ports Ports
	deps  module.Deps
}

// New returns the module; Init gives it the platform's services.
func New(p Ports) *Module {
	return &Module{Now: time.Now, Endpoints: sources.Production(), Marker: change.None{}, ports: p}
}

const modName = "routing"

func (*Module) Name() string      { return modName }
func (*Module) Migrations() fs.FS { return migrations.FS }

// interactiveRequest bounds each request made inside an admin request.
const interactiveRequest = 15 * time.Second

// Init builds the module's services.
func (m *Module) Init(d module.Deps) error {
	m.deps = d
	now := func() time.Time { return m.Now() }
	resolver := &sources.Resolver{Endpoints: m.Endpoints, Fetch: &sources.Fetcher{Timeout: interactiveRequest}}
	// The guard of custom saves is the lists', built right after: the
	// closures read m.Lists at call time.
	m.Services = services.New(services.Deps{
		DB: d.DB, Events: d.Events, Resolver: resolver, Marker: m, Now: now, Log: d.Log,
		Check: func(ctx context.Context, tx *sqlx.Tx, id int64, set snapshot.Set) error {
			return m.Lists.CheckService(ctx, tx, id, set)
		},
		Refused: func(ctx context.Context, err error, actor string) { m.Lists.Refused(ctx, err, actor) },
	})
	m.Lists = lists.New(lists.Deps{
		DB: d.DB, Events: d.Events, Services: m.Services, Hostnames: m.ports.Hostnames, Marker: m, Now: now, Log: d.Log,
	})
	return nil
}

// Mark forwards to m.Marker as it is at call time.
func (m *Module) Mark(ctx context.Context, tx *sqlx.Tx, c change.Change) error {
	return m.Marker.Mark(ctx, tx, c)
}

func (*Module) EventTypes() []events.Type { return Events }

func (*Module) SettingsSections() []settings.Section { return []settings.Section{conf.Section} }

func (*Module) Messages() i18n.Messages { return messages }

func (m *Module) Routes(r web.Routes) {
	pages.Register(r, pages.Deps{Services: m.Services, Lists: m.Lists, DB: m.deps.DB, Now: func() time.Time { return m.Now() }})
}

// Nav adds Lists and Services; Search, Discover, Routers and Shadowrocket come later.
func (*Module) Nav() []ui.NavItem {
	return []ui.NavItem{
		{Group: "routing", Label: "lists.nav", Href: "/routing/lists", Order: 10},
		{Group: "routing", Label: "services.nav", Href: "/routing/services", Order: 20},
	}
}

// Search finds routing lists by name, and services by tag, selector or name.
func (m *Module) Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []ui.SearchHit
	all, err := m.Lists.All(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range all {
		if q != "" && !strings.Contains(strings.ToLower(l.Name), q) {
			continue
		}
		n, err := m.Lists.Size(ctx, l.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, ui.SearchHit{Label: l.Name, Href: "/routing/lists/" + strconv.FormatInt(l.ID, 10), Meta: i18n.N(ctx, "lists.search", int64(n))})
		if len(out) == limit {
			return out, nil
		}
	}
	rows, err := m.Services.List(ctx, services.Filter{})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if q != "" && !strings.Contains(r.Tag, q) && !strings.Contains(strings.ToLower(r.Selector), q) &&
			!strings.Contains(strings.ToLower(r.Name), q) {
			continue
		}
		out = append(out, ui.SearchHit{
			Label: r.Tag, Href: "/routing/services/" + strconv.FormatInt(r.ID, 10),
			Meta: i18n.N(ctx, "services.search", int64(r.Count), i18n.Args{"source": string(r.Source)}),
		})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// SubjectTypes are the subject types of the module's events.
func (*Module) SubjectTypes() []string {
	return []string{"service", "routing_list", "routing"}
}

// NameSubjects names subjects for Activity, Jobs and notifications.
func (m *Module) NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error) {
	out := map[string]ui.SubjectRef{}
	for _, id := range ids {
		if typ == "routing" {
			switch id {
			case "refresh":
				out[id] = ui.SubjectRef{Label: i18n.T(ctx, "routing.subject.refresh"), Href: "/routing/services"}
			case "catalog":
				out[id] = ui.SubjectRef{Label: i18n.T(ctx, "routing.subject.catalog"), Href: "/routing/search"}
			}
			continue
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		switch typ {
		case "service":
			if s, err := m.Services.Get(ctx, n); err == nil {
				out[id] = ui.SubjectRef{Label: s.Tag, Href: "/routing/services/" + id}
			}
		case "routing_list":
			if name, err := store.ListName(ctx, m.deps.DB.R, n); err == nil {
				out[id] = ui.SubjectRef{Label: name, Href: "/routing/lists/" + id}
			}
		}
	}
	return out, nil
}

var (
	_ module.Module           = (*Module)(nil)
	_ module.Initializer      = (*Module)(nil)
	_ module.EventDeclarer    = (*Module)(nil)
	_ module.SettingsDeclarer = (*Module)(nil)
	_ module.MessagesDeclarer = (*Module)(nil)
	_ module.RouteDeclarer    = (*Module)(nil)
	_ module.NavDeclarer      = (*Module)(nil)
	_ module.Searcher         = (*Module)(nil)
	_ module.SubjectNamer     = (*Module)(nil)
	_ change.Marker           = (*Module)(nil)
)

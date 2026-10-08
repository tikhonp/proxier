// Package subscriptions is the module that gives people and devices access
// to servers: subscriptions (ordered sets of servers) and the links that
// serve them (docs/modules/subscriptions.md). This file is wiring only. The
// module talks to servers only through its ports (ADR 0002).
package subscriptions

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/migrations"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/pages"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Ports are what the module uses of other modules. A nil one hides its features.
type Ports struct {
	Catalog servers.EndpointCatalog
}

// Module is the subscriptions module.
type Module struct {
	// Now is the module's clock; every service reads it, so a test replaces it once.
	Now  func() time.Time
	Subs *subs.Service

	ports Ports
	deps  module.Deps
}

// New returns the module; Init gives it the platform's services.
func New(p Ports) *Module { return &Module{Now: time.Now, ports: p} }

func (*Module) Name() string      { return modName }
func (*Module) Migrations() fs.FS { return migrations.FS }

// Init builds the module's services.
func (m *Module) Init(d module.Deps) error {
	m.deps = d
	now := func() time.Time { return m.Now() }
	m.Subs = subs.New(subs.Deps{
		DB: d.DB, Events: d.Events, Settings: d.Settings, I18n: d.I18n, Catalog: m.ports.Catalog, Now: now, Log: d.Log,
	})
	return nil
}

func (*Module) EventTypes() []events.Type { return Events }

func (*Module) SettingsSections() []settings.Section { return []settings.Section{conf.Section} }

// Messages merges the module's texts with the stub texts of output.
func (*Module) Messages() i18n.Messages {
	all := i18n.Messages{}
	for _, set := range []i18n.Messages{messages, output.Messages} {
		for k, v := range set {
			all[k] = v
		}
	}
	return all
}

// Subscribers: subscriptions follow servers being activated and retired.
func (m *Module) Subscribers() []events.Subscriber { return []events.Subscriber{m.Subs.Subscriber()} }

func (m *Module) Routes(r web.Routes) {
	pages.Register(r, pages.Deps{Subs: m.Subs, DB: m.deps.DB, Now: func() time.Time { return m.Now() }})
}

// Nav adds Subscriptions (Links arrives in 2b).
func (*Module) Nav() []ui.NavItem {
	return []ui.NavItem{{Group: "subscriptions", Label: "subs.nav", Href: "/subscriptions", GoKey: "u", Order: 10}}
}

// Search finds subscriptions by name or title.
func (m *Module) Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	rows, err := m.Subs.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []ui.SearchHit
	for _, r := range rows {
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) && !strings.Contains(strings.ToLower(r.Title), q) {
			continue
		}
		out = append(out, ui.SearchHit{
			Label: r.Name, Meta: i18n.N(ctx, "subs.search", int64(len(r.Members))),
			Href: "/subscriptions/" + strconv.FormatInt(r.ID, 10),
		})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// SubjectTypes are the subject types of the module's events (links: 2b).
func (*Module) SubjectTypes() []string { return []string{"subscription"} }

// NameSubjects names subscriptions for Activity, Jobs and notifications.
func (m *Module) NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error) {
	out := map[string]ui.SubjectRef{}
	if typ != "subscription" {
		return out, nil
	}
	for _, id := range ids {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		if s, err := m.Subs.Get(ctx, n); err == nil {
			out[id] = ui.SubjectRef{Label: s.Name, Href: "/subscriptions/" + id}
		}
	}
	return out, nil
}

var (
	_ module.Module             = (*Module)(nil)
	_ module.Initializer        = (*Module)(nil)
	_ module.EventDeclarer      = (*Module)(nil)
	_ module.SettingsDeclarer   = (*Module)(nil)
	_ module.MessagesDeclarer   = (*Module)(nil)
	_ module.RouteDeclarer      = (*Module)(nil)
	_ module.NavDeclarer        = (*Module)(nil)
	_ module.SubscriberDeclarer = (*Module)(nil)
	_ module.SubjectNamer       = (*Module)(nil)
	_ module.Searcher           = (*Module)(nil)
)

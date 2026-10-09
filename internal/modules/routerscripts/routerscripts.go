// Package routerscripts is the module that hosts RouterOS setup scripts in
// versions and fills them in for new routers (docs/modules/router-scripts.md).
// This file is wiring only. The module talks to subscriptions and routing
// only through their ports (ADR 0002).
package routerscripts

import (
	"context"
	"io/fs"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/migrations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/pages"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Ports are what the module uses of other modules. A nil one hides its
// features: no Subscription link section without Links, no Register for
// routing section without Routers.
type Ports struct {
	Links   subscriptions.LinkIssuer
	Routers routing.RouterRegistrar
}

// Module is the router scripts module.
type Module struct {
	// Now is the module's clock; every service reads it, so a test replaces it once.
	Now     func() time.Time
	Scripts *scripts.Service
	// Generations fills versions in for routers (4b).
	Generations *generations.Service

	ports Ports
	deps  module.Deps
}

// New returns the module; Init gives it the platform's services.
func New(p Ports) *Module { return &Module{Now: time.Now, ports: p} }

const modName = "routerscripts"

func (*Module) Name() string      { return modName }
func (*Module) Migrations() fs.FS { return migrations.FS }

// Init builds the module's services.
func (m *Module) Init(d module.Deps) error {
	m.deps = d
	now := func() time.Time { return m.Now() }
	m.Scripts = scripts.NewService(scripts.Deps{DB: d.DB, Events: d.Events, Now: now, Log: d.Log})
	m.Generations = generations.NewService(generations.Deps{
		DB: d.DB, Vault: d.Vault, Events: d.Events, SSH: d.SSH, Tailnet: d.Tailnet, Scripts: m.Scripts,
		Links: m.ports.Links, Routers: m.ports.Routers, I18n: d.I18n, BaseURL: d.Cfg.BaseURL, Now: now, Log: d.Log,
	})
	return nil
}

func (*Module) EventTypes() []events.Type { return Events }

// Messages are the module's texts with params' (the findings).
func (*Module) Messages() i18n.Messages {
	out := maps.Clone(messages)
	maps.Copy(out, params.Messages())
	maps.Copy(out, generationMessages)
	return out
}

func (m *Module) Routes(r web.Routes) {
	m.Generations.Register(r.Public)
	pages.Register(r, pages.Deps{Scripts: m.Scripts, Generations: m.Generations, DB: m.deps.DB, Now: func() time.Time { return m.Now() }})
}

// JobTypes is the fetch URLs' expiry scan.
func (m *Module) JobTypes() []jobs.Type { return m.Generations.JobTypes() }

// Schedules runs it every 5 minutes.
func (m *Module) Schedules() []jobs.Schedule { return m.Generations.Schedules() }

// Nav adds Scripts to the router scripts group (no go-to key: one / away).
func (*Module) Nav() []ui.NavItem {
	return []ui.NavItem{{Group: "routerscripts", Label: "rscripts.nav", Href: pages.ListPath, Order: 10}}
}

// Search finds scripts that aren't archived by name or slug (with the
// action Generate for a new router when they have a version), and
// generations by router name.
func (m *Module) Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	rows, err := m.Scripts.List(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []ui.SearchHit
	for _, r := range rows {
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) && !strings.Contains(r.Slug, q) {
			continue
		}
		meta := i18n.N(ctx, "rscripts.search", int64(r.Generations), i18n.Args{"version": r.Current})
		if r.Current == 0 {
			meta = i18n.N(ctx, "rscripts.search.none", int64(r.Generations))
		}
		out = append(out, ui.SearchHit{Label: r.Name, Href: pages.ScriptHref(r.ID), Meta: meta})
		if r.Current > 0 {
			out = append(out, ui.SearchHit{Label: i18n.T(ctx, "rscripts.search.generate", i18n.Args{"name": r.Name}),
				Href: pages.GenerateHref(r.ID), Meta: i18n.T(ctx, "rscripts.search.action")})
		}
		if len(out) >= limit {
			return out[:limit], nil
		}
	}
	gens, err := m.Generations.Search(ctx, q, limit-len(out))
	if err != nil {
		return nil, err
	}
	for _, g := range gens {
		out = append(out, ui.SearchHit{Label: g.RouterName, Href: pages.GenerationHref(g.ID),
			Meta: i18n.T(ctx, "rscripts.search.generation", i18n.Args{"script": g.Script, "version": g.Version})})
	}
	return out, nil
}

// SubjectTypes are the subject types of the module's events.
func (*Module) SubjectTypes() []string { return []string{"router_script", "generation"} }

// NameSubjects names scripts by their name and generations as "Dacha ·
// fresh-router v4". A deleted script has no reference: its events carry its
// name.
func (m *Module) NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error) {
	out := map[string]ui.SubjectRef{}
	for _, id := range ids {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		switch typ {
		case "router_script":
			if s, err := store.GetScript(ctx, m.deps.DB.R, n); err == nil {
				out[id] = ui.SubjectRef{Label: s.Name, Href: pages.ScriptHref(n)}
			}
		case "generation":
			g, err := store.GetGeneration(ctx, m.deps.DB.R, n)
			if err != nil {
				continue
			}
			label := g.RouterName
			if s, err := store.GetScript(ctx, m.deps.DB.R, g.ScriptID); err == nil {
				label += " · " + s.Name + " v" + strconv.Itoa(g.Version)
			}
			out[id] = ui.SubjectRef{Label: label, Href: pages.GenerationHref(n)}
		}
	}
	return out, nil
}

var (
	_ module.Module               = (*Module)(nil)
	_ module.Initializer          = (*Module)(nil)
	_ module.EventDeclarer        = (*Module)(nil)
	_ module.MessagesDeclarer     = (*Module)(nil)
	_ module.RouteDeclarer        = (*Module)(nil)
	_ module.NavDeclarer          = (*Module)(nil)
	_ module.Searcher             = (*Module)(nil)
	_ module.SubjectNamer         = (*Module)(nil)
	_ module.JobDeclarer          = (*Module)(nil)
	_ module.NotificationRenderer = (*Module)(nil)
)

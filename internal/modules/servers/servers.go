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

	"github.com/tikhonp/proxier/internal/modules/servers/agent"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/migrations"
	"github.com/tikhonp/proxier/internal/modules/servers/pages"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/retire"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/stats"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/modules/servers/validate"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
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
	// Provision builds servers: the form, Create, the job, Retry, Activate anyway.
	Provision *provision.Service
	// Deploy changes active servers: plans, redeploys, rollouts, operations, rotation.
	Deploy *deploy.Service
	// Stats keeps the metric samples; Health runs the checks and decides the verdict.
	Stats  *stats.Service
	Health *health.Service
	// Retire takes servers out of service for good.
	Retire *retire.Service
	// Agent hands template drafts to coding agents (1h).
	Agent *agent.Service
	// DNS writes and removes servers' A records; Waiter waits for resolvers to
	// show them (1d composes both into provisioning).
	DNS    dns.Driver
	Waiter dns.Waiter
	// CloudflareClient makes the API client for a token; tests point it at
	// cloudflaretest.
	CloudflareClient func(token string) *cloudflare.Client

	usage UsageReader
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
	m.CloudflareClient = cloudflare.New
	m.DNS = cloudflare.NewDriver(d.Settings, func(token string) *cloudflare.Client { return m.CloudflareClient(token) })
	m.Waiter = dns.NewWaiter()
	m.Provision = provision.New(provision.Deps{
		DB: d.DB, Events: d.Events, Vault: d.Vault, Jobs: d.Jobs, SSH: d.SSH, Settings: d.Settings,
		Store: m.Store, Templates: m.Templates, Log: d.Log,
		// read at use: tests replace the module's driver and waiter
		DNS: func() dns.Driver { return m.DNS }, Waiter: func() dns.Waiter { return m.Waiter },
	})
	m.Deploy = deploy.New(deploy.Deps{
		DB: d.DB, Events: d.Events, Vault: d.Vault, Jobs: d.Jobs, SSH: d.SSH, Settings: d.Settings,
		Store: m.Store, Templates: m.Templates, Log: d.Log,
		// the seams are provisioning's, read at use: a test replaces them once
		Seams: func() deploy.Seams {
			p := m.Provision
			return deploy.Seams{ProxyTest: p.ProxyTest, ProxyOptions: p.ProxyOptions, RemoteEnv: p.RemoteEnv, Attempts: p.SmokeAttempts, Gap: p.SmokeGap}
		},
	})
	m.Stats = stats.New(d.DB, d.Settings)
	m.Health = health.New(health.Deps{
		DB: d.DB, Events: d.Events, Vault: d.Vault, Jobs: d.Jobs, SSH: d.SSH, Settings: d.Settings, I18n: d.I18n,
		Stats: m.Stats, Log: d.Log,
		Setup: func(ctx context.Context, id int64) (health.Setup, error) {
			cs, err := m.Deploy.CheckSetup(ctx, id)
			return health.Setup{Server: cs.Server, Dir: cs.Dir, Checks: cs.Checks, ProxyTestURL: cs.ProxyTestURL}, err
		},
		// the seams are provisioning's, read at use: a test replaces them once
		Seams: func() health.Seams {
			p := m.Provision
			return health.Seams{ProxyTest: p.ProxyTest, ProxyOptions: p.ProxyOptions, RemoteEnv: p.RemoteEnv}
		},
	})
	m.Retire = retire.New(retire.Deps{
		DB: d.DB, Events: d.Events, Jobs: d.Jobs, SSH: d.SSH, Store: m.Store, Log: d.Log,
		Setup: func(ctx context.Context, id int64) (retire.Setup, error) {
			cs, err := m.Deploy.CheckSetup(ctx, id)
			return retire.Setup{Server: cs.Server, Dir: cs.Dir, Uninstall: cs.Uninstall}, err
		},
		// read at use: tests replace the module's driver and the seams
		DNS:   func() dns.Driver { return m.DNS },
		Usage: func() retire.UsageReader { return m.usage },
		RemoteEnv: func(log *jobs.Logger) remote.Env {
			if f := m.Provision.RemoteEnv; f != nil {
				return f(log)
			}
			return remote.Env{Log: log}
		},
	})
	m.Agent = agent.New(agent.Deps{
		DB: d.DB, Events: d.Events, Vault: d.Vault, Templates: m.Templates, Jobs: d.Jobs, Log: d.Log,
		BaseURL: strings.TrimRight(d.Cfg.BaseURL.String(), "/"),
		// read at use: the masked context of a real server for a draft
		Preview: func(ctx context.Context, serverID int64) (func(*manifest.Manifest, string) (render.Context, error), error) {
			return m.Provision.PreviewContext(ctx, serverID, false)
		},
	})
	// An agent session ends in the transaction that publishes or discards its draft.
	m.Templates.DraftEnded = m.Agent.CloseForDraft
	// The validators that need the embedded xray and the endpoint types.
	validate.XrayConfig, validate.EndpointFields = proxy.ValidateConfig, endpoint.Check
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

// JobTypes: provisioning (1d), the changes to active servers (1e), the health
// checks (1f) and retirement (1g).
func (m *Module) JobTypes() []jobs.Type {
	types := append([]jobs.Type{m.Provision.JobType()}, m.Deploy.JobTypes()...)
	types = append(types, m.Health.JobTypes()...)
	types = append(types, m.Agent.JobTypes()...)
	return append(types, m.Retire.JobType())
}

// Subscribers: the rollout advances when a deploy job of its running item ends.
func (m *Module) Subscribers() []events.Subscriber { return []events.Subscriber{m.Deploy.Subscriber()} }

func (m *Module) Schedules() []jobs.Schedule {
	return append(m.Health.Schedules(), m.Agent.Schedule())
}

func (*Module) SettingsSections() []settings.Section {
	return []settings.Section{Section, cloudflare.Section}
}

// SettingsPages: Settings → Servers (Cloudflare lives under Integrations).
func (*Module) SettingsPages() []ui.SettingsPage {
	return []ui.SettingsPage{{Slug: "servers", Title: "settings.servers", Order: 35}}
}

// Integrations adds the Cloudflare row to Settings → Integrations.
func (m *Module) Integrations(ctx context.Context) []ui.IntegrationRow {
	row := ui.IntegrationRow{Name: "Cloudflare", Href: "/settings/integrations/cloudflare", State: i18n.T(ctx, "integrations.not_configured")}
	token, err := m.deps.Settings.Get(ctx, cloudflare.TokenKey)
	if err != nil {
		m.deps.Log.Error("servers: cloudflare settings", "error", err)
		return []ui.IntegrationRow{row}
	}
	if token != "" {
		zones, _ := m.deps.Settings.Get(ctx, cloudflare.ZonesKey)
		row.On = true
		row.State = i18n.T(ctx, "servers.cloudflare.state", i18n.Args{"n": len(dns.SplitZones(zones))})
	}
	return []ui.IntegrationRow{row}
}

// Messages merges the module's texts: the core table and the template pages'.
func (*Module) Messages() i18n.Messages {
	all := make(i18n.Messages, len(messages)+len(templateMessages)+len(serverMessages)+len(deployMessages)+len(healthMessages)+len(retireMessages)+len(agentMessages))
	for _, set := range []i18n.Messages{messages, templateMessages, serverMessages, deployMessages, healthMessages, retireMessages, agentMessages} {
		for k, v := range set {
			all[k] = v
		}
	}
	return all
}

func (m *Module) Routes(r web.Routes) {
	// The agent API answers to a bearer token only: a public route.
	m.Agent.Routes(r.Public)
	pages.Register(r, pages.Deps{
		Store: m.Store, Templates: m.Templates, Log: m.deps.Log, Settings: m.deps.Settings, DNS: m.DNS,
		CloudflareClient: func(token string) *cloudflare.Client { return m.CloudflareClient(token) },
		Vault:            m.deps.Vault, Jobs: m.deps.Jobs, Provision: m.Provision, Deploy: m.Deploy, Retire: m.Retire, Health: m.Health, Stats: m.Stats, Agent: m.Agent,
		Usage: func() pages.UsageReader { return m.usage },
	})
}

// Nav adds the module's entries; each sub-phase adds its own with its page.
func (*Module) Nav() []ui.NavItem {
	return []ui.NavItem{
		{Group: "servers", Label: "servers.nav", Href: "/servers", GoKey: "s", Order: 10},
		{Group: "servers", Label: "templates.nav", Href: "/templates", GoKey: "t", Order: 20},
		{Group: "servers", Label: "locations.nav", Href: "/locations", Order: 30},
	}
}

// Search finds templates by name or slug and locations by code or name.
func (m *Module) Search(ctx context.Context, q string, limit int) ([]ui.SearchHit, error) {
	lang := i18n.From(ctx).Lang
	q = strings.ToLower(strings.TrimSpace(q))
	var out []ui.SearchHit
	tpls, err := m.Templates.List(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, t := range tpls {
		if q != "" && !strings.Contains(strings.ToLower(t.Slug), q) && !strings.Contains(strings.ToLower(t.Name), q) {
			continue
		}
		meta := i18n.T(ctx, "templates.search")
		if t.Archived {
			meta += " · " + i18n.T(ctx, "templates.archived")
		}
		out = append(out, ui.SearchHit{Label: t.Name + " · " + t.Slug, Meta: meta, Href: "/templates/" + strconv.FormatInt(t.ID, 10)})
		if len(out) == limit {
			return out, nil
		}
	}
	all, err := store.ListServers(ctx, m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	for _, sv := range all {
		if q != "" && !strings.Contains(strings.ToLower(sv.Name), q) && !strings.Contains(sv.IP, q) &&
			!strings.Contains(strings.ToLower(sv.ProxyHostname), q) && !strings.Contains(strings.ToLower(sv.ManagementHostname), q) {
			continue
		}
		meta := i18n.T(ctx, "servers.search")
		if sv.State != "active" {
			meta += " · " + i18n.T(ctx, "servers.state."+sv.State)
		}
		out = append(out, ui.SearchHit{Label: country.Flag(sv.Country) + " " + sv.Name + " · " + sv.IP, Meta: meta, Href: "/servers/" + strconv.FormatInt(sv.ID, 10)})
		if len(out) == limit {
			return out, nil
		}
	}
	locs, err := m.Store.Locations(ctx)
	if err != nil {
		return nil, err
	}
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
func (*Module) SubjectTypes() []string {
	return []string{"server", "template", "location", "home", "rollout"}
}

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
		case "rollout":
			out[id] = ui.SubjectRef{Label: i18n.T(ctx, "servers.rollout.subject", i18n.Args{"id": id}), Href: "/servers/rollouts/" + id}
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
	_ module.Module               = (*Module)(nil)
	_ module.Initializer          = (*Module)(nil)
	_ module.Migrated             = (*Module)(nil)
	_ module.EventDeclarer        = (*Module)(nil)
	_ module.SettingsDeclarer     = (*Module)(nil)
	_ module.SettingsPageDeclarer = (*Module)(nil)
	_ module.IntegrationDeclarer  = (*Module)(nil)
	_ module.MessagesDeclarer     = (*Module)(nil)
	_ module.RouteDeclarer        = (*Module)(nil)
	_ module.JobDeclarer          = (*Module)(nil)
	_ module.SubscriberDeclarer   = (*Module)(nil)
	_ module.DashboardDeclarer    = (*Module)(nil)
	_ module.NotificationRenderer = (*Module)(nil)
	_ module.NavDeclarer          = (*Module)(nil)
	_ module.SubjectNamer         = (*Module)(nil)
	_ module.Searcher             = (*Module)(nil)
)

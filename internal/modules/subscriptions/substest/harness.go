package substest

import (
	"context"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// Harness is the app with the module on a fake catalog.
type Harness struct {
	T       *testing.T
	Site    *sitetest.Site
	App     *platform.App
	Mod     *subscriptions.Module
	Login   *sitetest.Login
	Catalog *Catalog
	Now     time.Time // the module's clock (starts 2026-10-07 12:00 UTC); Advance moves it
}

// New: the app with the module on a fake catalog holding nl-1 (🇳🇱 Netherlands 1,
// id 1) and de-1 (🇩🇪 Germany 1, id 2), general.admin_contact "@tikhonp". No
// job workers run.
func New(t *testing.T) *Harness {
	t.Helper()
	cat := NewCatalog()
	cat.Put(Server(1, "nl-1", "🇳🇱", "Netherlands", 1))
	cat.Put(Server(2, "de-1", "🇩🇪", "Germany", 1))
	mod := subscriptions.New(subscriptions.Ports{Catalog: cat})
	site := sitetest.New(t, sitetest.Options{Modules: []module.Module{mod}})
	h := &Harness{T: t, Site: site, App: site.App, Mod: mod, Catalog: cat, Now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	mod.Now = func() time.Time { return h.Now }
	if err := h.App.Settings.Set(context.Background(), "admin", "general", map[string]string{"general.admin_contact": "@tikhonp"}); err != nil {
		t.Fatal(err)
	}
	h.Login = site.SignIn("")
	return h
}

// Advance moves the module's clock.
func (h *Harness) Advance(d time.Duration) { h.Now = h.Now.Add(d) }

// Events lists the recorded events of a type, oldest first.
func (h *Harness) Events(typ string) []events.Event {
	h.T.Helper()
	list, err := events.List(context.Background(), h.App.DB.R, events.Filter{Type: typ, Limit: 1000})
	if err != nil {
		h.T.Fatal(err)
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}

// Subscription creates one with the given servers, in order.
func (h *Harness) Subscription(name string, serverIDs ...int64) int64 {
	h.T.Helper()
	ctx := context.Background()
	id, err := h.Mod.Subs.Create(ctx, name, "", "", "admin")
	if err != nil {
		h.T.Fatal(err)
	}
	for _, sid := range serverIDs { // one at a time: AddServers sorts by name
		if err := h.Mod.Subs.AddServers(ctx, id, []int64{sid}, "admin"); err != nil {
			h.T.Fatal(err)
		}
	}
	return id
}

// Exec runs SQL on the write connection (link rows before 2b builds links).
func (h *Harness) Exec(q string, args ...any) {
	h.T.Helper()
	if _, err := h.App.DB.W.ExecContext(context.Background(), q, args...); err != nil {
		h.T.Fatalf("%s: %v", q, err)
	}
}

// WithServers: the real servers module (serverstest, stubbed proxy) plus this
// module wired as main.go wires it.
func WithServers(t *testing.T) (*serverstest.Harness, *subscriptions.Module) {
	t.Helper()
	var sm *subscriptions.Module
	h := serverstest.NewHarness(t, serverstest.StubProxy(), serverstest.WithModules(func(m *servers.Module) []module.Module {
		sm = subscriptions.New(subscriptions.Ports{Catalog: m.EndpointCatalog()})
		m.SetUsage(sm.Usage())
		return []module.Module{sm}
	}))
	return h, sm
}

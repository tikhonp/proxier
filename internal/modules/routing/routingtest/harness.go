// Package routingtest is the routing module's test harness: the whole app
// (sitetest) with the module on fake servers ports and a sourcestest
// upstream, the module's clock in the test's hands.
package routingtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/sources/sourcestest"
	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// Harness is the app with the module.
type Harness struct {
	T       *testing.T
	Site    *sitetest.Site
	App     *platform.App
	Mod     *routing.Module
	Login   *sitetest.Login
	Up      *sourcestest.Upstream
	Servers *Servers         // fake servers ports (3b, 3g)
	Marks   *change.Recorder // the marker until 3e
	Now     time.Time        // the module's clock, starting 2026-10-08 12:00 UTC
}

// Option changes New.
type Option func(*options)

type options struct{ noServers bool }

// NoServers builds the module without servers ports: nothing to guard,
// discovery direct only.
func NoServers() Option { return func(o *options) { o.noServers = true } }

// New opens the app with the module, a fresh upstream and a recording marker.
// No job workers run.
func New(t *testing.T, opts ...Option) *Harness {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	srv := &Servers{}
	ports := routing.Ports{}
	if !o.noServers {
		ports.Hostnames, ports.Catalog = srv, srv
	}
	mod := routing.New(ports)
	up := sourcestest.New(t)
	mod.Endpoints = up.Endpoints()
	marks := &change.Recorder{}
	mod.Marker = marks
	site := sitetest.New(t, sitetest.Options{Modules: []module.Module{mod}})
	h := &Harness{T: t, Site: site, App: site.App, Mod: mod, Up: up, Servers: srv, Marks: marks,
		Now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	mod.Now = func() time.Time { return h.Now }
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

// Upstream adds the selector as a service; Up must already serve it.
func (h *Harness) Upstream(sel string) int64 {
	h.T.Helper()
	id, err := h.Mod.Services.Add(context.Background(), sel, "admin")
	if err != nil {
		h.T.Fatalf("add %s: %v", sel, err)
	}
	return id
}

// Custom adds a custom service; names as the editor takes them
// ("example.com", "full:api.example.com").
func (h *Harness) Custom(tag string, names ...string) int64 {
	h.T.Helper()
	ctx := context.Background()
	id, err := h.Mod.Services.CreateCustom(ctx, services.Custom{Name: tag, Tag: tag}, "admin")
	if err != nil {
		h.T.Fatalf("create %s: %v", tag, err)
	}
	if len(names) == 0 {
		return id
	}
	rows := make([]services.DomainRow, 0, len(names))
	for _, n := range names {
		rows = append(rows, services.DomainRow{Domain: n, Exact: strings.HasPrefix(n, "full:")})
	}
	if _, err := h.Mod.Services.SaveCustom(ctx, id, services.Edit{Name: tag, Tag: tag, Rows: rows}, "admin"); err != nil {
		h.T.Fatalf("save %s: %v", tag, err)
	}
	return id
}

// Exec runs SQL on the write connection (list memberships before 3b).
func (h *Harness) Exec(q string, args ...any) {
	h.T.Helper()
	if _, err := h.App.DB.W.ExecContext(context.Background(), q, args...); err != nil {
		h.T.Fatalf("%s: %v", q, err)
	}
}

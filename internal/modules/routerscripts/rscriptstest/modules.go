package rscriptstest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routerscripts"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/sources/sourcestest"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// Modules is the app with the real subscriptions (on substest's fake
// catalog holding nl-1 and de-1) and routing (on routingtest's fake servers
// ports and a sourcestest upstream) modules, wired as main.go wires them;
// one clock for the three modules and the jobs.
type Modules struct {
	*Harness
	Subs    *subscriptions.Module
	Routing *routing.Module
	Catalog *substest.Catalog
	Up      *sourcestest.Upstream

	jobsMu   sync.Mutex
	stopJobs func()
}

// WithModules opens the app with the three modules. No job workers run.
func WithModules(t *testing.T) *Modules {
	t.Helper()
	h := &Harness{T: t, Now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	cat := substest.NewCatalog()
	cat.Put(substest.Server(1, "nl-1", "🇳🇱", "Netherlands", 1))
	cat.Put(substest.Server(2, "de-1", "🇩🇪", "Germany", 1))
	subs := subscriptions.New(subscriptions.Ports{Catalog: cat})
	srv := &routingtest.Servers{}
	rt := routing.New(routing.Ports{Hostnames: srv, Catalog: srv, Dialer: srv})
	up := sourcestest.New(t)
	rt.Endpoints = up.Endpoints()
	mod := routerscripts.New(routerscripts.Ports{Links: subs.LinkIssuer(), Routers: rt.RouterRegistrar()})
	site := sitetest.New(t, sitetest.Options{Modules: []module.Module{subs, rt, mod}})
	h.Site, h.App, h.Mod = site, site.App, mod
	subs.Now, rt.Now, mod.Now = h.clock, h.clock, h.clock
	h.App.Jobs.Now = h.clock
	h.Login = site.SignIn("")
	return &Modules{Harness: h, Subs: subs, Routing: rt, Catalog: cat, Up: up}
}

// Subscription creates one with nl-1; its id.
func (m *Modules) Subscription(name string) int64 {
	m.T.Helper()
	ctx := context.Background()
	id, err := m.Subs.Subs.Create(ctx, name, "", "", "admin")
	if err != nil {
		m.T.Fatal(err)
	}
	if err := m.Subs.Subs.AddServers(ctx, id, []int64{1}, "admin"); err != nil {
		m.T.Fatal(err)
	}
	return id
}

// Link creates a link (English, no expiry) in the subscription; its id.
func (m *Modules) Link(name string, sub int64) int64 {
	m.T.Helper()
	id, err := m.Subs.Links.Create(context.Background(), links.New{Name: name, SubscriptionID: sub, Lang: "en"}, "admin")
	if err != nil {
		m.T.Fatal(err)
	}
	return id
}

// StartJobs runs the job workers with fast polls until the test ends (the
// scheduler polls only hourly: tests queue schedules with RunSchedule).
func (m *Modules) StartJobs() {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	if m.stopJobs != nil {
		return
	}
	m.App.Jobs.Poll, m.App.Jobs.SchedulerPoll, m.App.Jobs.Grace = 100*time.Millisecond, time.Hour, 300*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.App.Jobs.Start(ctx) }()
	m.stopJobs = func() { cancel(); <-done }
	m.T.Cleanup(func() {
		m.jobsMu.Lock()
		stop := m.stopJobs
		m.stopJobs = nil
		m.jobsMu.Unlock()
		if stop != nil {
			stop()
		}
	})
}

// Drain waits until no job is queued or running.
func (m *Modules) Drain() {
	m.T.Helper()
	m.App.Jobs.Kick()
	deadline := time.Now().Add(60 * time.Second)
	calm := 0
	for time.Now().Before(deadline) {
		var n int
		if err := m.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE state IN ('running', 'interrupted', 'queued') AND run_after <= ?`,
			m.clock().UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
			m.T.Fatal(err)
		}
		if n == 0 {
			if calm++; calm >= 3 {
				return
			}
		} else {
			calm = 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.T.Fatal("jobs did not become idle")
}

// RunSchedule enqueues any module's schedule now, as the scheduler would;
// the job's id.
func (m *Modules) RunSchedule(name string) int64 {
	m.T.Helper()
	ctx := context.Background()
	for _, mod := range m.App.Modules {
		jd, ok := mod.(module.JobDeclarer)
		if !ok {
			continue
		}
		for _, s := range jd.Schedules() {
			if s.Name != name {
				continue
			}
			req, err := s.Request(ctx)
			if err != nil {
				m.T.Fatal(err)
			}
			req.CreatedBy = "schedule:" + name
			e, err := m.App.Jobs.EnqueueNow(ctx, req)
			if err != nil {
				m.T.Fatal(err)
			}
			return e.ID
		}
	}
	m.T.Fatalf("no schedule %s", name)
	return 0
}

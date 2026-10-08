// Package routingtest is the routing module's test harness: the whole app
// (sitetest) with the module on fake servers ports and a sourcestest
// upstream, the module's clock in the test's hands.
package routingtest

import (
	"context"
	"strings"
	"sync"
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
	Servers *Servers         // fake servers ports (the guard, 3g)
	Marks   *change.Recorder // the marker until 3e
	Now     time.Time        // the module's clock, starting 2026-10-08 12:00 UTC; move it with Advance

	mu       sync.Mutex
	stopJobs func()
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
	mod.Now = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.Now
	}
	h.Login = site.SignIn("")
	return h
}

// Advance moves the module's clock.
func (h *Harness) Advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Now = h.Now.Add(d)
}

// StartJobs runs the job workers (the scheduler polls only hourly: tests
// queue schedules with RunSchedule).
func (h *Harness) StartJobs() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopJobs != nil {
		return
	}
	h.App.Jobs.Poll, h.App.Jobs.SchedulerPoll, h.App.Jobs.Grace = 100*time.Millisecond, time.Hour, 300*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.App.Jobs.Start(ctx) }()
	h.stopJobs = func() { cancel(); <-done }
	h.T.Cleanup(h.StopJobs)
}

// StopJobs stops the workers as a shutdown does: running jobs are
// interrupted, to resume on the next StartJobs.
func (h *Harness) StopJobs() {
	h.mu.Lock()
	stop := h.stopJobs
	h.stopJobs = nil
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Drain waits until no job is queued, running or interrupted.
func (h *Harness) Drain() {
	h.T.Helper()
	deadline := time.Now().Add(60 * time.Second)
	calm := 0
	for time.Now().Before(deadline) {
		var n int
		if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE state IN ('running', 'interrupted', 'queued')`); err != nil {
			h.T.Fatal(err)
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
	h.T.Fatal("jobs did not become idle")
}

// RunSchedule enqueues that schedule's job now, as the scheduler would, and
// returns its id.
func (h *Harness) RunSchedule(name string) int64 {
	h.T.Helper()
	ctx := context.Background()
	for _, s := range h.Mod.Schedules() {
		if s.Name != name {
			continue
		}
		req, err := s.Request(ctx)
		if err != nil {
			h.T.Fatal(err)
		}
		req.CreatedBy = "schedule:" + name
		e, err := h.App.Jobs.EnqueueNow(ctx, req)
		if err != nil {
			h.T.Fatal(err)
		}
		return e.ID
	}
	h.T.Fatalf("no schedule %s", name)
	return 0
}

// Job reads a job's state and error.
func (h *Harness) Job(id int64) (state, errText string) {
	h.T.Helper()
	j, err := h.App.Jobs.Job(context.Background(), id)
	if err != nil {
		h.T.Fatal(err)
	}
	return string(j.State), j.Error
}

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

// List creates a list with these services in order and returns its id;
// "Main" is the list made by the migration, reused (its services are added
// after any it has).
func (h *Harness) List(name string, serviceIDs ...int64) int64 {
	h.T.Helper()
	ctx := context.Background()
	var id int64
	if err := h.App.DB.R.Get(&id, `SELECT coalesce(max(id), 0) FROM routing_lists WHERE name = ?`, name); err != nil {
		h.T.Fatal(err)
	}
	if id == 0 {
		var err error
		if id, err = h.Mod.Lists.Create(ctx, name, "", "admin"); err != nil {
			h.T.Fatalf("create list %s: %v", name, err)
		}
	}
	// one at a time: Add sorts what it adds by tag
	for _, sid := range serviceIDs {
		if err := h.Mod.Lists.Add(ctx, id, []int64{sid}, "admin"); err != nil {
			h.T.Fatalf("add %d to %s: %v", sid, name, err)
		}
	}
	return id
}

// Exec runs SQL on the write connection.
func (h *Harness) Exec(q string, args ...any) {
	h.T.Helper()
	if _, err := h.App.DB.W.ExecContext(context.Background(), q, args...); err != nil {
		h.T.Fatalf("%s: %v", q, err)
	}
}

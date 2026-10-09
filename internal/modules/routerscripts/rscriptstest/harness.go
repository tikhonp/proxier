// Package rscriptstest is the router scripts module's test harness: the
// whole app (sitetest) with the module on empty ports (4b: fake ones), the
// module's clock in the test's hands.
package rscriptstest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routerscripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// Harness is the app with the module.
type Harness struct {
	T     *testing.T
	Site  *sitetest.Site
	App   *platform.App
	Mod   *routerscripts.Module
	Login *sitetest.Login
	Now   time.Time // the module's clock (starts 2026-10-09 12:00 UTC); Advance moves it

	mu sync.Mutex
}

// New opens the app with the module on empty ports. No job workers run.
func New(t *testing.T) *Harness {
	t.Helper()
	mod := routerscripts.New(routerscripts.Ports{})
	site := sitetest.New(t, sitetest.Options{Modules: []module.Module{mod}})
	h := &Harness{T: t, Site: site, App: site.App, Mod: mod, Now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
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

// Exec runs a statement on the write connection (rows 4b's code would write).
func (h *Harness) Exec(q string, args ...any) {
	h.T.Helper()
	if _, err := h.App.DB.W.ExecContext(context.Background(), q, args...); err != nil {
		h.T.Fatalf("%s: %v", q, err)
	}
}

// Script creates a script with body and publishes it (warnings confirmed);
// its id.
func (h *Harness) Script(name string, body []byte) int64 {
	h.T.Helper()
	ctx := context.Background()
	id, err := h.Mod.Scripts.Create(ctx, scripts.New{Name: name, Body: string(body)}, events.ActorAdmin)
	if err != nil {
		h.T.Fatalf("create %s: %v", name, err)
	}
	if _, err := h.Mod.Scripts.Publish(ctx, id, 1, "", true, events.ActorAdmin); err != nil {
		h.T.Fatalf("publish %s: %v", name, err)
	}
	return id
}

// Publish saves body as the script's draft (making one from the current
// version when needed) and publishes it with the warnings confirmed; the new
// version's number.
func (h *Harness) Publish(id int64, body []byte) int {
	h.T.Helper()
	ctx := context.Background()
	d, err := h.Mod.Scripts.EditDraft(ctx, id, events.ActorAdmin)
	if err != nil {
		h.T.Fatal(err)
	}
	rev, err := h.Mod.Scripts.SaveDraft(ctx, id, d.Revision, string(body), events.ActorAdmin)
	if err != nil {
		h.T.Fatal(err)
	}
	n, err := h.Mod.Scripts.Publish(ctx, id, rev, "", true, events.ActorAdmin)
	if err != nil {
		h.T.Fatal(err)
	}
	return n
}

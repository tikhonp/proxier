package substest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/fetch"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/db"
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
	// Rotator is the fake servers.Rotator (nil with NoRotator).
	Rotator *Rotator
	Now     time.Time // the module's clock (starts 2026-10-07 12:00 UTC); Advance moves it

	mu       sync.Mutex
	stopJobs func()
	fed      int64
}

// Option changes New.
type Option func(*options)

type options struct{ noRotator bool }

// NoRotator builds the module without a Rotator: Cut off is hidden.
func NoRotator() Option { return func(o *options) { o.noRotator = true } }

// Tunnel is the trusted proxy's address: requests from it carry the client's
// X-Real-IP.
const Tunnel = "127.0.0.1:41000"

// New: the app with the module on a fake catalog holding nl-1 (🇳🇱 Netherlands 1,
// id 1) and de-1 (🇩🇪 Germany 1, id 2), general.admin_contact "@tikhonp", the
// tunnel (127.0.0.1) trusted, and a fake Rotator whose job type FakeRotate is
// registered. No job workers run. The public rate limiter runs on the harness
// clock.
func New(t *testing.T, opts ...Option) *Harness {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	cat := NewCatalog()
	cat.Put(Server(1, "nl-1", "🇳🇱", "Netherlands", 1))
	cat.Put(Server(2, "de-1", "🇩🇪", "Germany", 1))
	ports := subscriptions.Ports{Catalog: cat}
	var rot *Rotator
	if !o.noRotator {
		rot = &Rotator{}
		ports.Rotator = rot
	}
	mod := subscriptions.New(ports)
	site := sitetest.New(t, sitetest.Options{Modules: []module.Module{mod}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	if err := site.App.Jobs.Register("fake", fakeRotateType()); err != nil {
		t.Fatal(err)
	}
	h := &Harness{T: t, Site: site, App: site.App, Mod: mod, Catalog: cat, Rotator: rot, Now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	mod.Now = func() time.Time { return h.Now }
	h.App.PublicLimit.Now = func() time.Time { return h.Now }
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

// Link creates a link through the service (language EN, no expiry) and
// returns its id and token.
func (h *Harness) Link(subID int64, name string) (int64, string) {
	h.T.Helper()
	ctx := context.Background()
	id, err := h.Mod.Links.Create(ctx, links.New{Name: name, SubscriptionID: subID, Lang: "en"}, "admin")
	if err != nil {
		h.T.Fatal(err)
	}
	token, err := h.Mod.Links.Token(ctx, id)
	if err != nil {
		h.T.Fatal(err)
	}
	return id, token
}

// Fetch requests path from addr ("198.51.100.23:5000" when empty) with a
// user agent.
func (h *Harness) Fetch(method, path, addr, ua string) *httptest.ResponseRecorder {
	h.T.Helper()
	if addr == "" {
		addr = "198.51.100.23:5000"
	}
	hd := http.Header{}
	if ua != "" {
		hd.Set("User-Agent", ua)
	}
	return h.Site.Do(sitetest.Req{Method: strings.ToUpper(method), Path: path, Addr: addr, Header: hd})
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
		sm = subscriptions.New(subscriptions.Ports{Catalog: m.EndpointCatalog(), Rotator: m.Rotator()})
		m.SetUsage(sm.Usage())
		return []module.Module{sm}
	}))
	return h, sm
}

// StartJobs runs the job workers with fast polls until the test ends. Job
// steps read the module's clock, so tests still move time with Advance.
func (h *Harness) StartJobs() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopJobs != nil {
		return
	}
	h.App.Jobs.Poll, h.App.Jobs.SchedulerPoll, h.App.Jobs.Grace = 5*time.Millisecond, time.Hour, 300*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.App.Jobs.Start(ctx) }()
	h.stopJobs = func() { cancel(); <-done }
	h.T.Cleanup(func() {
		h.mu.Lock()
		stop := h.stopJobs
		h.stopJobs = nil
		h.mu.Unlock()
		if stop != nil {
			stop()
		}
	})
}

// Drain waits until no job is queued or running.
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

// FetchAt inserts a fetch row directly (link, time, IP, user agent), with the
// network and app computed as a real fetch would, outcome ok.
func (h *Harness) FetchAt(linkID int64, at time.Time, ip, ua string) {
	h.T.Helper()
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		h.T.Fatal(err)
	}
	if _, err := store.InsertFetch(context.Background(), h.App.DB.W, store.Fetch{
		LinkID: linkID, At: db.At(at), IP: ip, Network: fetch.Network(addr), UserAgent: fetch.TrimUA(ua),
		App: fetch.Detect(ua), Format: "uri-plain", Outcome: "ok",
	}); err != nil {
		h.T.Fatal(err)
	}
}

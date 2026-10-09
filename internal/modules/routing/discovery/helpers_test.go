package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery/discoverytest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

var bg = context.Background()

// newH is the app with a fake browser and the job workers running.
func newH(t *testing.T, opts ...routingtest.Option) (*routingtest.Harness, *discoverytest.Browser) {
	t.Helper()
	b := discoverytest.New()
	h := routingtest.New(t, append([]routingtest.Option{routingtest.WithBrowser(b)}, opts...)...)
	h.StartJobs()
	return h, b
}

func start(t *testing.T, h *routingtest.Harness, st discovery.Start) int64 {
	t.Helper()
	id, err := h.Mod.Discovery.Start(bg, st, "admin")
	if err != nil {
		t.Fatalf("start %+v: %v", st, err)
	}
	return id
}

// wait waits until the run reaches the state.
func wait(t *testing.T, h *routingtest.Harness, id int64, state string) discovery.Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		r, err := h.Mod.Discovery.Get(bg, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == state {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d is %s, not %s (%s)", id, r.State, state, r.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// refreshCatalog fills the catalog from the harness's upstream.
func refreshCatalog(t *testing.T, h *routingtest.Harness) {
	t.Helper()
	id, err := h.Mod.Catalog.RefreshNow(bg, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if st, e := h.Job(id); st != "succeeded" {
		t.Fatalf("catalog job: %s %s", st, e)
	}
}

func req(url, host string) discovery.Request {
	return discovery.Request{URL: url, Host: host, Type: "Script", Status: 200}
}

func failed(url, host, why string) discovery.Request {
	return discovery.Request{URL: url, Host: host, Type: "Script", Failed: why}
}

func loaded(url, title string, reqs ...discovery.Request) discovery.Result {
	return discovery.Result{Title: title, Pages: []discovery.Page{{URL: url, Loaded: true, Status: 200, Screenshot: []byte{0xff, 0xd8, 0xff}}}, Requests: reqs}
}

func hostOf(r discovery.Run, name string) (discovery.Host, bool) {
	for _, h := range r.Hosts {
		if h.Host == name {
			return h, true
		}
	}
	return discovery.Host{}, false
}

func groupOf(r discovery.Run, reg string) (discovery.Group, bool) {
	for _, g := range r.Groups {
		if g.Registrable == reg {
			return g, true
		}
	}
	return discovery.Group{}, false
}

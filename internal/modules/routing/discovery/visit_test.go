package discovery_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestVisitThroughServerUsesDialer(t *testing.T) {
	far := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "far side") }))
	defer far.Close()
	h, b := newH(t)
	h.Servers.Put(1, "nl-1")
	h.Servers.Put(2, "de-1")
	b.Probe(far.URL)
	url := "https://example.com/"
	b.Site(url, "de-1", loaded(url, "Example", req(url, "example.com")))

	r := wait(t, h, start(t, h, discovery.Start{Website: "example.com", Via: discovery.ViaServer, ServerID: 2, Depth: 5}), discovery.Done)
	v := b.Visits()
	if len(v) != 1 || !strings.HasPrefix(v[0].Proxy, "socks5://127.0.0.1:") || v[0].Path != "de-1" || v[0].Links != 5 {
		t.Fatalf("visit: %+v", v)
	}
	// the browser's connection went through the run's listener to de-1's dialer
	if p := b.Probes(); len(p) != 1 || p[0] != "200 far side" {
		t.Errorf("probe through the listener: %v", p)
	}
	if d := h.Servers.Dialed(); !slices.Equal(d, []int64{2}) {
		t.Errorf("dialed %v", d)
	}
	if n := h.Servers.Open(); n != 0 {
		t.Errorf("%d dialers left open", n)
	}
	if len(r.Visits) != 1 || r.Visits[0].Path != "de-1" || !r.Visits[0].Loaded {
		t.Errorf("visits: %+v", r.Visits)
	}

	// an unhealthy or unknown server can't be chosen; without ports only Direct
	h.Servers.SetHealth(1, "blocked")
	for _, st := range []discovery.Start{
		{Website: "example.com", Via: discovery.ViaServer, ServerID: 1},
		{Website: "example.com", Via: discovery.ViaServer, ServerID: 42},
		{Website: "example.com", Via: "elsewhere"},
		{Website: "example.com", Via: discovery.ViaDirect, Depth: 3},
	} {
		if _, err := h.Mod.Discovery.Start(bg, st, "admin"); err == nil {
			t.Errorf("started %+v", st)
		}
	}
}

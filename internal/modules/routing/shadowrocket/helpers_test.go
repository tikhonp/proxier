package shadowrocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func bg() context.Context { return context.Background() }

const base = "[General]\ndns-server = system\n\n[Rule]\nRULE-SET,https://example.com/ads.list,REJECT\nFINAL,DIRECT\n"

const phoneUA = "Shadowrocket/2.2.62 CFNetwork/1568.100.1 Darwin/24.0.0"

// setup: Main with anthropic, and a config iphone following it.
func setup(t *testing.T, opts ...routingtest.Option) (*routingtest.Harness, int64) {
	t.Helper()
	h := routingtest.New(t, opts...)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	h.List("Main", h.Upstream("anthropic"))
	id, err := h.Mod.Shadowrocket.Create(bg(), shadowrocket.New{Name: "iphone", ListID: 1, Policy: "PROXY", Base: base}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return h, id
}

// fetch is a phone's request of a public path.
func fetch(h *routingtest.Harness, method, path string) *httptest.ResponseRecorder {
	return h.Site.Do(sitetest.Req{Method: method, Path: path, Addr: "198.51.100.23:5000", Header: http.Header{"User-Agent": {phoneUA}}})
}

// pathOf is the URL's path.
func pathOf(t *testing.T, h *routingtest.Harness, id int64) string {
	t.Helper()
	u, err := h.Mod.Shadowrocket.URL(bg(), id)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u, "http://proxier.test")
}

func fetches(t *testing.T, h *routingtest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM routing_shadowrocket_fetches`); err != nil {
		t.Fatal(err)
	}
	return n
}

func reqUA(path, ua string) sitetest.Req {
	return sitetest.Req{Method: "GET", Path: path, Addr: "198.51.100.23:5000", Header: http.Header{"User-Agent": {ua}}}
}

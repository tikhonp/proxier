package routingtest

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/sources/sourcestest"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/platform/module"
)

// WithServers builds the real servers module (serverstest, StubProxy) with
// subscriptions and routing wired as main.go wires them; routing's upstreams
// are a fresh sourcestest.
func WithServers(t *testing.T) (*serverstest.Harness, *routing.Module) {
	t.Helper()
	var rt *routing.Module
	up := sourcestest.New(t)
	h := serverstest.NewHarness(t, serverstest.StubProxy(), serverstest.WithModules(func(m *servers.Module) []module.Module {
		subs := subscriptions.New(subscriptions.Ports{Catalog: m.EndpointCatalog(), Rotator: m.Rotator()})
		m.SetUsage(subs.Usage())
		rt = routing.New(routing.Ports{Hostnames: m.ServerHostnames(), Catalog: m.EndpointCatalog(), Dialer: m.ProxyDialer()})
		rt.Endpoints = up.Endpoints()
		m.SetRouting(rt.Guard())
		return []module.Module{subs, rt}
	}))
	return h, rt
}

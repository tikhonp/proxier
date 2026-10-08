package routing

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/servers"
)

// Guard is servers' RoutingGuard: provisioning asks it whether a new
// server's hostnames are covered by any listed name. It works once Init has
// run; before that it covers nothing.
func (m *Module) Guard() servers.RoutingGuard { return guard{m} }

type guard struct{ m *Module }

func (g guard) Covering(ctx context.Context, hostnames []string) ([]servers.RoutedName, error) {
	if g.m.Lists == nil {
		return nil, nil
	}
	return g.m.Lists.Covering(ctx, hostnames)
}

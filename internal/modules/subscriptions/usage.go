package subscriptions

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/servers"
)

// Usage returns the port the servers module asks which subscriptions and
// links serve a server. It reads the service when called, so main.go may hand
// it over before Init.
func (m *Module) Usage() servers.UsageReader { return usage{m} }

type usage struct{ m *Module }

func (u usage) Usage(ctx context.Context, serverID int64) ([]string, int, error) {
	return u.m.Subs.Usage(ctx, serverID)
}

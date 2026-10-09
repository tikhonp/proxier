package routing

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// Integrations is the Chromium row on Settings → Integrations: connected
// with its version, not configured, or unreachable (asked for 3 s at most).
func (m *Module) Integrations(ctx context.Context) []ui.IntegrationRow {
	state, detail := m.Discovery.Chromium(ctx)
	return []ui.IntegrationRow{{
		Name: i18n.T(ctx, "discovery.integration"), Href: "/routing/discover", On: state == discovery.ChromiumConnected,
		State: i18n.T(ctx, "discovery.chromium."+state, i18n.Args{"detail": detail}),
	}}
}

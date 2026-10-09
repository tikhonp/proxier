package routing

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/routing/pages"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// Dashboard is the Routing area, after Links: routers that need a look
// first, the snapshots waiting for a decision, the last daily refresh.
// Nothing configured yet, no area.
func (m *Module) Dashboard(ctx context.Context) ([]ui.DashboardArea, error) {
	v, err := pages.Dashboard(ctx, m.pageDeps())
	if err != nil || v == nil {
		return nil, err
	}
	return []ui.DashboardArea{{Order: 40, Title: i18n.T(ctx, "routers.dash.title"), Body: pages.DashboardRouting(*v)}}, nil
}

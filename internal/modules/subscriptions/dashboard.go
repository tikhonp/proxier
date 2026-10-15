package subscriptions

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/pages"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// Dashboard is the Links area: links with a shared-link alert, then active
// links expiring within a week. Nothing to show, no area.
func (m *Module) Dashboard(ctx context.Context, w access.Who) ([]ui.DashboardArea, error) {
	area, err := pages.Dashboard(ctx, m.pageDeps(), w)
	if err != nil || area == nil {
		return nil, err
	}
	return []ui.DashboardArea{*area}, nil
}

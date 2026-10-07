package servers

import (
	"context"
	"sort"
	"strconv"

	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/pages"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// Dashboard is the servers area: counts by lifecycle state and the servers
// that failed, with the step they stopped at. 1f extends it with health.
func (m *Module) Dashboard(ctx context.Context) ([]ui.DashboardArea, error) {
	counts, err := store.CountByState(ctx, m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return nil, nil
	}
	all, err := store.ListServers(ctx, m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	var failed []store.Server
	for _, s := range all {
		if s.State == "failed" {
			failed = append(failed, s)
		}
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].Name < failed[j].Name })
	return []ui.DashboardArea{{
		Order: 20, Title: i18n.T(ctx, "servers.dash.title"),
		Body: pages.DashboardServers(counts, failed),
	}}, nil
}

// RenderNotification gives server.activated its two texts: ready, and active
// without a passing proxy test when the admin forced it. The other types use
// the default text.
func (m *Module) RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error) {
	if e.Type != "server.activated" {
		return notify.Message{}, false, nil
	}
	id, err := strconv.ParseInt(e.Subject.ID, 10, 64)
	if err != nil {
		return notify.Message{}, false, nil
	}
	s, err := store.GetServer(ctx, m.deps.DB.R, id)
	if err != nil {
		return notify.Message{}, false, nil
	}
	name := country.Flag(s.Country) + " " + s.Name
	forced, _ := e.Payload["forced"].(bool)
	if forced {
		return notify.Message{Title: loc.T("notify.server.activated.forced", i18n.Args{"subject": name})}, true, nil
	}
	return notify.Message{Title: loc.T("notify.server.activated", i18n.Args{"subject": name}) + " (" + s.LocationName + " " + strconv.Itoa(s.Number) + ")"}, true, nil
}

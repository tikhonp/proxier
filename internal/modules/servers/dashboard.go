package servers

import (
	"context"
	"sort"
	"strconv"

	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/pages"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// Dashboard is the servers area: home's banner, the fleet by health, the
// servers that need a look (with their reason and since), servers with an
// update available, the top three by disk and by traffic today, and the
// first-run items that belong to servers.
func (m *Module) Dashboard(ctx context.Context) ([]ui.DashboardArea, error) {
	loc := i18n.From(ctx)
	counts, err := store.CountByState(ctx, m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	v := pages.DashboardView{Counts: counts, Health: map[string]int{}}
	for _, n := range counts {
		v.Total += n
	}
	token, err := m.deps.Settings.Get(ctx, cloudflare.TokenKey)
	if err != nil {
		return nil, err
	}
	v.Checklist = []pages.DashCheck{
		{Label: i18n.T(ctx, "health.dash.setup_cloudflare"), Href: "/settings/integrations/cloudflare", Done: token != ""},
		{Label: i18n.T(ctx, "health.dash.setup_server"), Href: "/servers/new", Done: v.Total > 0},
	}
	if token != "" && v.Total > 0 {
		v.Checklist = nil
	}
	if v.Total == 0 && token != "" {
		v.Checklist = v.Checklist[1:]
	}
	home, since, err := store.GetHome(ctx, m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	if v.Total > 0 {
		v.Home = home
		if !since.IsZero() {
			v.HomeSince = loc.Ago(since.Time)
		}
	}
	all, err := store.ListServers(ctx, m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	defaults := map[int64]int{}
	var active []store.Server
	for _, s := range all {
		switch s.State {
		case "failed":
			v.Failed = append(v.Failed, pages.DashRow{ID: s.ID, Name: s.Name,
				Reason: i18n.T(ctx, "job.servers.provision.step."+s.FailedStep) + " · " + s.FailedError})
		case "active":
			active = append(active, s)
			v.Health[s.Health]++
			if s.Health != "healthy" {
				kind := map[string]string{"degraded": "look", "blocked": "blocked", "down": "broken", "unknown": "unknown", "paused": "paused"}[s.Health]
				reason := i18n.T(ctx, "servers.health."+s.Health)
				if r, ok := health.ParseReason(s.HealthReason); ok {
					reason += ": " + health.RenderReason(loc, r)
				}
				row := pages.DashRow{ID: s.ID, Name: s.Name, Flag: country.Flag(s.Country), Kind: kind, Word: i18n.T(ctx, "servers.health."+s.Health), Reason: reason}
				if !s.HealthSince.IsZero() {
					row.Note = i18n.T(ctx, "servers.since", i18n.Args{"ago": loc.Ago(s.HealthSince.Time)})
				}
				v.NotHealthy = append(v.NotHealthy, row)
			}
			if _, ok := defaults[s.TemplateID]; !ok {
				if info, err := m.Templates.Get(ctx, s.TemplateID); err == nil {
					defaults[s.TemplateID] = info.DefaultVersion
				}
			}
			if d := defaults[s.TemplateID]; d > s.TemplateVersion {
				v.Updates = append(v.Updates, pages.DashRow{ID: s.ID, Name: s.Name, Flag: country.Flag(s.Country),
					Note: i18n.T(ctx, "health.dash.update_row", i18n.Args{"version": "v" + strconv.Itoa(d)})})
			}
		}
	}
	if err := m.dashTops(ctx, active, &v); err != nil {
		return nil, err
	}
	if v.Total == 0 && len(v.Checklist) == 0 {
		return nil, nil
	}
	return []ui.DashboardArea{{
		Order: 20, Title: i18n.T(ctx, "servers.dash.title"),
		Body: pages.DashboardServers(v),
	}}, nil
}

// dashTops ranks the active servers by disk use and by traffic today.
func (m *Module) dashTops(ctx context.Context, active []store.Server, v *pages.DashboardView) error {
	samples, err := store.LatestSamples(ctx, m.deps.DB.R)
	if err != nil {
		return err
	}
	byID := map[int64]store.Server{}
	for _, s := range active {
		byID[s.ID] = s
	}
	type rank struct {
		s   store.Server
		val float64
	}
	var disk []rank
	for _, sm := range samples {
		if s, ok := byID[sm.ServerID]; ok && sm.DiskTotal > 0 {
			disk = append(disk, rank{s, float64(sm.DiskUsed) / float64(sm.DiskTotal) * 100})
		}
	}
	sort.SliceStable(disk, func(i, j int) bool { return disk[i].val > disk[j].val })
	for i, r := range disk {
		if i == 3 {
			break
		}
		v.TopDisk = append(v.TopDisk, pages.DashRank{ID: r.s.ID, Name: r.s.Name, Value: strconv.Itoa(int(r.val+0.5)) + " %"})
	}
	var traffic []rank
	for _, s := range active {
		rx, tx, err := m.Stats.TrafficToday(ctx, s.ID)
		if err != nil {
			return err
		}
		if rx+tx > 0 {
			traffic = append(traffic, rank{s, float64(rx + tx)})
		}
	}
	sort.SliceStable(traffic, func(i, j int) bool { return traffic[i].val > traffic[j].val })
	for i, r := range traffic {
		if i == 3 {
			break
		}
		v.TopTraffic = append(v.TopTraffic, pages.DashRank{ID: r.s.ID, Name: r.s.Name, Value: pages.HumanBytes(int64(r.val))})
	}
	return nil
}

// RenderNotification gives server.activated its two texts (ready, and active
// without a passing proxy test when the admin forced it) and
// server.health_changed its state and reason in the admin's language. The
// other types use the default text.
func (m *Module) RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error) {
	id, err := strconv.ParseInt(e.Subject.ID, 10, 64)
	if err != nil {
		return notify.Message{}, false, nil
	}
	switch e.Type {
	case "server.activated", "server.health_changed":
	default:
		return notify.Message{}, false, nil
	}
	s, err := store.GetServer(ctx, m.deps.DB.R, id)
	if err != nil {
		return notify.Message{}, false, nil
	}
	name := country.Flag(s.Country) + " " + s.Name
	if e.Type == "server.health_changed" {
		to, _ := e.Payload["to"].(string)
		emoji := map[string]string{"down": "🔴", "blocked": "🟣", "healthy": "🟢"}[to]
		if emoji == "" || !loc.Has("notify.server.health_changed."+to) {
			return notify.Message{}, false, nil
		}
		msg := notify.Message{Emoji: emoji, Title: loc.T("notify.server.health_changed."+to, i18n.Args{"subject": name})}
		if key, _ := e.Payload["reason_key"].(string); key != "" && to != "healthy" {
			args, _ := e.Payload["reason_args"].(map[string]any)
			msg.Body = health.RenderReason(loc, health.Reason{Key: key, Args: args})
		}
		return msg, true, nil
	}
	forced, _ := e.Payload["forced"].(bool)
	if forced {
		return notify.Message{Title: loc.T("notify.server.activated.forced", i18n.Args{"subject": name})}, true, nil
	}
	return notify.Message{Title: loc.T("notify.server.activated", i18n.Args{"subject": name}) + " (" + s.LocationName + " " + strconv.Itoa(s.Number) + ")"}, true, nil
}

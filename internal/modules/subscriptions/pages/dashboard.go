package pages

import (
	"context"
	"sort"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/alerts"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// dashWithin is how soon an expiry shows on the dashboard.
const dashWithin = 7 * 24 * time.Hour

type dashRow struct {
	ID               int64
	Name, Kind, Word string
	Note             string
}

// Dashboard is the Links area: links with an alert (and their counts now),
// then active links expiring within a week, soonest first. Nil when there is
// nothing to show.
func Dashboard(ctx context.Context, d Deps) (*ui.DashboardArea, error) {
	h := &handler{Deps: d}
	loc := h.loc(ctx)
	now := d.Now()
	list, err := d.Links.List(ctx, links.Filter{})
	if err != nil {
		return nil, err
	}
	counts, err := store.WindowCounts(ctx, d.DB.R, db.At(now.Add(-alerts.Window)), db.At(now), 0)
	if err != nil {
		return nil, err
	}
	byLink := map[int64]store.WindowCount{}
	for _, c := range counts {
		byLink[c.LinkID] = c
	}
	var alerted, expiring []dashRow
	var soon []links.Row
	for _, r := range list {
		if d.Alerts.HasAlert(r.Link, now) {
			c := byLink[r.ID]
			alerted = append(alerted, dashRow{ID: r.ID, Name: r.Name, Kind: "look", Word: loc.T("alerts.word"),
				Note: loc.T("alerts.dash.counts", i18n.Args{
					"networks": loc.N("alerts.dash.networks", int64(c.Networks)), "apps": loc.N("alerts.dash.apps", int64(c.Apps)),
				})})
		}
		if r.Status(now) == "active" && !r.Expires.IsZero() && r.Expires.Sub(now) <= dashWithin {
			soon = append(soon, r)
		}
	}
	sort.SliceStable(soon, func(i, j int) bool { return soon[i].Expires.Before(soon[j].Expires) })
	for _, r := range soon {
		expiring = append(expiring, dashRow{ID: r.ID, Name: r.Name, Kind: "off", Word: loc.T("links.dash.expiring"), Note: expiryShort(loc, r.Link, now)})
	}
	if len(alerted)+len(expiring) == 0 {
		return nil, nil
	}
	return &ui.DashboardArea{Order: 30, Title: loc.T("links.title"), Body: dashboardLinks(append(alerted, expiring...))}, nil
}

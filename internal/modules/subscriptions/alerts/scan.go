package alerts

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Job types of the package.
const (
	JobSharedScan = "subscriptions.shared_scan"
	JobCountries  = "subscriptions.network_countries"
)

// Scan raises link.shared_suspected for each link that more networks or apps
// than its limits fetched in the 24 hours up to now, unless it is muted or
// alerted less than 24 hours ago. Links without fetches in the window are
// not looked at; disabled and deleted ones are (stub fetches count too).
func (s *Service) Scan(ctx context.Context, now time.Time, actor string) error {
	at := db.At(now)
	counts, err := store.WindowCounts(ctx, s.d.DB.R, db.At(now.Add(-Window)), at, 0)
	if err != nil || len(counts) == 0 {
		return err
	}
	defNetworks, defApps, err := s.defaults(ctx)
	if err != nil {
		return err
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, c := range counts {
			l, err := store.GetLink(ctx, tx, c.LinkID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			lim := limitsOf(l.AlertNetworks, l.AlertApps, l.AlertsMuted, defNetworks, defApps)
			if lim.Muted || (c.Networks <= lim.Networks && c.Apps <= lim.Apps) {
				continue
			}
			if !l.AlertedAt.IsZero() && now.Sub(l.AlertedAt.Time) < Window {
				continue
			}
			if err := store.SetAlertedAt(ctx, tx, l.ID, at); err != nil {
				return err
			}
			if _, err := s.d.Events.Record(ctx, tx, events.Event{
				Time: at, Type: "link.shared_suspected", Subject: links.Subject(l.ID), Actor: actor,
				Payload: map[string]any{"networks": c.Networks, "apps": c.Apps, "window": "24h"},
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// JobTypes: the shared-link scan and the country lookup, both quiet and run
// once (the next scan or fetch is the retry).
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{
		{Name: JobSharedScan, Queue: jobs.Maintenance, MaxAttempts: 1, Quiet: true, Steps: []jobs.Step{
			{Name: "scan", Run: func(ctx context.Context, r *jobs.Run) error { return s.Scan(ctx, s.d.Now(), r.Info().Actor()) }},
		}},
		{Name: JobCountries, Queue: jobs.Maintenance, MaxAttempts: 1, Quiet: true, Steps: []jobs.Step{
			{Name: "lookup", Run: func(ctx context.Context, _ *jobs.Run) error { return s.LookupCountries(ctx, s.d.Now()) }},
		}},
	}
}

// Schedules runs the shared-link scan every 15 minutes; country lookups are
// queued by fetches.
func (s *Service) Schedules() []jobs.Schedule {
	return []jobs.Schedule{{Name: JobSharedScan, Every: 15 * time.Minute, Jitter: time.Minute,
		Request: func(context.Context) (jobs.Request, error) {
			return jobs.Request{Type: JobSharedScan, CoalescingKey: "shared_scan"}, nil
		}}}
}

package links

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// JobExpiryScan warns before links expire, notifies when they have, erases
// ended tombstones' tokens and prunes old fetches
// (docs/processes/subscriptions/link-lifecycle.md, the expiry scan).
const JobExpiryScan = "subscriptions.expiry_scan"

// ExpiryStep records link.expired for active links past their expiry, then
// link.expiring_soon for those expiring within subscriptions.expiry_warning,
// each once per expiry (SetExpiry re-arms both). Expired ones go first, so a
// link that expires before it was warned only gets link.expired. Disabled and
// deleted links are skipped: a disabled one that expired notifies once it is
// enabled again.
func (s *Service) ExpiryStep(ctx context.Context, now time.Time, actor string) error {
	warning, err := s.d.Settings.GetDuration(ctx, conf.ExpiryWarning)
	if err != nil {
		return err
	}
	at := db.At(now)
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		expired, err := store.ExpiredUnmarked(ctx, tx, at)
		if err != nil {
			return err
		}
		for _, d := range expired {
			if err := store.MarkExpired(ctx, tx, d.ID, at); err != nil {
				return err
			}
			if err := s.recordAt(ctx, tx, at, "link.expired", d.ID, actor, map[string]any{"expiry": timeText(d.ExpiresAt.Time)}); err != nil {
				return err
			}
		}
		soon, err := store.ExpiringUnwarned(ctx, tx, at, db.At(now.Add(warning)))
		if err != nil {
			return err
		}
		for _, d := range soon {
			if err := store.MarkWarned(ctx, tx, d.ID, at); err != nil {
				return err
			}
			if err := s.recordAt(ctx, tx, at, "link.expiring_soon", d.ID, actor, map[string]any{"expiry": timeText(d.ExpiresAt.Time)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// TombstoneStep erases the token of deleted links whose tombstone has ended.
// It records nothing: their URL has answered 404 since that moment already.
func (s *Service) TombstoneStep(ctx context.Context, now time.Time) error {
	period, err := s.Tombstone(ctx)
	if err != nil {
		return err
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := store.EraseTombstones(ctx, tx, db.At(now.Add(-period)))
		return err
	})
}

// PruneStep deletes fetches older than subscriptions.fetch_retention and
// network countries past their freshness, so a network still in use is
// looked up again.
func (s *Service) PruneStep(ctx context.Context, now time.Time) error {
	retention, err := s.d.Settings.GetDuration(ctx, conf.FetchRetention)
	if err != nil {
		return err
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.PruneFetches(ctx, tx, db.At(now.Add(-retention))); err != nil {
			return err
		}
		_, err := store.PruneCountries(ctx, tx, db.At(now.Add(-store.CountryFresh)), db.At(now.Add(-store.CountryRetry)))
		return err
	})
}

// JobType is the expiry scan: quiet, once (the next scan is the retry).
func (s *Service) JobType() jobs.Type {
	return jobs.Type{Name: JobExpiryScan, Queue: jobs.Maintenance, MaxAttempts: 1, Quiet: true, Steps: []jobs.Step{
		{Name: "expiry", Run: func(ctx context.Context, r *jobs.Run) error { return s.ExpiryStep(ctx, s.d.Now(), r.Info().Actor()) }},
		{Name: "tombstones", Run: func(ctx context.Context, _ *jobs.Run) error { return s.TombstoneStep(ctx, s.d.Now()) }},
		{Name: "prune", Run: func(ctx context.Context, _ *jobs.Run) error { return s.PruneStep(ctx, s.d.Now()) }},
	}}
}

// Schedule runs the expiry scan every 15 minutes.
func (s *Service) Schedule() jobs.Schedule {
	return jobs.Schedule{Name: JobExpiryScan, Every: 15 * time.Minute, Jitter: time.Minute,
		Request: func(context.Context) (jobs.Request, error) {
			return jobs.Request{Type: JobExpiryScan, CoalescingKey: "expiry_scan"}, nil
		}}
}

func (s *Service) recordAt(ctx context.Context, tx *sqlx.Tx, at db.Time, typ string, id int64, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: at, Type: typ, Subject: Subject(id), Actor: actor, Payload: payload})
	return err
}

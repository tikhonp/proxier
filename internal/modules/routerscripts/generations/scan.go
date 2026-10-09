package generations

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// JobFetchURLs is the expiry scan of fetch URLs, and its schedule.
const JobFetchURLs = "routerscripts.fetch_urls"

// ScanEvery is how often the scan runs: a constant, as the lifetime is.
const ScanEvery = 5 * time.Minute

// Expire is the scan's step: every waiting URL past its hour becomes
// expired, its token erased, with routerscript.fetch_url_expired, in one
// transaction. The fetch never waits for it: such a URL already answers 404.
func (s *Service) Expire(ctx context.Context, actor string) (int, error) {
	now := db.At(s.d.Now())
	n := 0
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		n = 0
		list, err := store.ExpiredFetchURLs(ctx, tx, now)
		if err != nil {
			return err
		}
		for _, u := range list {
			if err := s.expire(ctx, tx, u, now, actor); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// JobTypes is the scan's job: maintenance, one attempt, quiet.
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{{Name: JobFetchURLs, Queue: jobs.Maintenance, MaxAttempts: 1, Quiet: true, Steps: []jobs.Step{
		{Name: "expire", Run: func(ctx context.Context, r *jobs.Run) error {
			_, err := s.Expire(ctx, r.Info().Actor())
			return err
		}},
	}}}
}

// Schedules runs the scan every ScanEvery.
func (s *Service) Schedules() []jobs.Schedule {
	return []jobs.Schedule{{Name: JobFetchURLs, Every: ScanEvery, Request: func(context.Context) (jobs.Request, error) {
		return jobs.Request{Type: JobFetchURLs, CoalescingKey: "fetch_urls"}, nil
	}}}
}

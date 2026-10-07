package jobs

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

const (
	JobRetention       = 30 * 24 * time.Hour
	FailedJobRetention = 90 * 24 * time.Hour
	SignInRetention    = 30 * 24 * time.Hour
)

// PlatformTypes are the job types and schedules the platform itself owns.
func (s *System) PlatformTypes() ([]Type, []Schedule) {
	types := []Type{
		{
			Name: "platform.retention", Queue: Maintenance, MaxAttempts: 1,
			Steps: []Step{
				{Name: "jobs", Run: func(ctx context.Context, r *Run) error {
					n, err := s.PruneJobs(ctx)
					r.Log().Info("Removed %d finished jobs", n)
					return err
				}},
				{Name: "signin", Run: func(ctx context.Context, r *Run) error {
					a, b, err := s.PruneSignIn(ctx)
					r.Log().Info("Removed %d sign-in attempts and %d old sessions", a, b)
					return err
				}},
			},
		},
		demoType(),
	}
	schedules := []Schedule{{
		Name: "platform.retention", At: "05:00",
		Request: func(context.Context) (Request, error) { return Request{Type: "platform.retention"}, nil },
	}}
	return types, schedules
}

// PruneJobs deletes finished jobs past their retention, with their steps and
// lines: failed ones after 90 days, the others after 30, and clean quiet ones
// after a day.
func (s *System) PruneJobs(ctx context.Context) (n int64, err error) {
	now := s.Now()
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM jobs WHERE
			  (state = 'failed' AND finished_at < ?)
			  OR (state IN ('succeeded', 'cancelled') AND finished_at < ?)
			  OR (quiet = 1 AND state = 'succeeded' AND attempt <= 1 AND finished_at < ?)`,
			db.At(now.Add(-FailedJobRetention)), db.At(now.Add(-JobRetention)), db.At(now.Add(-QuietRetention)))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// PruneSignIn deletes sign-in attempts and ended or expired sessions older
// than 30 days.
func (s *System) PruneSignIn(ctx context.Context) (attempts, sessions int64, err error) {
	cutoff := db.At(s.Now().Add(-SignInRetention))
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM sign_in_attempts WHERE time < ?`, cutoff)
		if err != nil {
			return err
		}
		attempts, _ = res.RowsAffected()
		res, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE (ended_at IS NOT NULL AND ended_at < ?) OR expires_at < ?`, cutoff, cutoff)
		if err != nil {
			return err
		}
		sessions, _ = res.RowsAffected()
		return nil
	})
	return
}

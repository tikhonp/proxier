package routers

import (
	"context"
	"errors"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Statuses of a router's sync, as its marker and word show them.
const (
	StatusSynced   = "synced"
	StatusSyncing  = "syncing"
	StatusFailed   = "failed"
	StatusNever    = "never"    // connected, never synced
	StatusUntested = "untested" // saved untested: waits for a passing test
)

// Now is what a router's jobs are doing.
type Now struct {
	Syncing bool      // a sync runs
	Queued  bool      // a sync waits to start
	StartAt time.Time // when the queued sync starts
	Retry   bool      // the queued sync is a retry after a failed attempt
	Attempt int       // the retry's next attempt
	JobID   int64     // the running or queued sync
	Testing bool      // a test runs or waits
	Preview bool      // a preview runs or waits
}

// Busy reads what the router's jobs are doing.
func (s *Service) Busy(ctx context.Context, id int64) (Now, error) {
	held, err := s.d.Jobs.Holding(ctx, resourceKey(id))
	if err != nil {
		return Now{}, err
	}
	var n Now
	for _, h := range held {
		switch h.Type {
		case JobTest:
			n.Testing = true
		case JobPreview:
			n.Preview = true
		case JobSync:
			if h.State == jobs.Running {
				n.Syncing, n.JobID = true, h.ID
				continue
			}
			if n.Queued {
				continue
			}
			j, err := s.d.Jobs.Job(ctx, h.ID)
			if errors.Is(err, jobs.ErrNotFound) {
				continue
			} else if err != nil {
				return Now{}, err
			}
			n.Queued, n.StartAt, n.JobID = true, j.RunAfter.Time, h.ID
			if j.Attempt > 0 {
				n.Retry, n.Attempt = true, j.Attempt+1
			}
		}
	}
	return n, nil
}

// StatusOf is a router's sync status.
func StatusOf(r Router, syncing bool) string {
	switch {
	case syncing:
		return StatusSyncing
	case r.LastResult == "failed":
		return StatusFailed
	case r.LastResult == "synced":
		return StatusSynced
	case !r.Connected():
		return StatusUntested
	}
	return StatusNever
}

// LastFailure is the router's newest failed sync; false when its last sync
// didn't fail.
func (s *Service) LastFailure(ctx context.Context, id int64) (Sync, bool, error) {
	r, err := store.LatestSync(ctx, s.d.DB.R, id, "sync")
	if errors.Is(err, store.ErrNotFound) {
		return Sync{}, false, nil
	}
	if err != nil || r.State != "failed" {
		return Sync{}, false, err
	}
	return syncOf(r), true, nil
}

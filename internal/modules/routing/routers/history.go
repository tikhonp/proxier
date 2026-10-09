package routers

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Sync is a sync, preview (3f: drift check, removal) of a router.
type Sync struct {
	ID                                           int64
	RouterID                                     int64
	JobID                                        int64
	Kind                                         string // sync, preview (3f: drift, removal)
	Trigger                                      string
	State                                        string // running, done, failed, cancelled
	Step                                         string
	Hop                                          string
	Error                                        string
	ReadAt                                       time.Time
	Plan                                         []TagPlan
	Script                                       string // preview
	Added, Updated, Removed, Unchanged, Recorded int
	StartedAt, FinishedAt                        time.Time
}

// Pushed counts what a sync pushed.
func (s Sync) Pushed() int { return s.Added + s.Updated + s.Removed }

func syncOf(r store.Sync) Sync {
	s := Sync{
		ID: r.ID, RouterID: r.RouterID, JobID: r.JobID, Kind: r.Kind, Trigger: r.Trigger, State: r.State, Step: r.Step, Hop: r.Hop,
		Error: r.Error, ReadAt: r.ReadAt.Time, Script: r.Script, Added: r.Added, Updated: r.Updated, Removed: r.Removed,
		Unchanged: r.Unchanged, Recorded: r.Recorded, StartedAt: r.StartedAt.Time, FinishedAt: r.FinishedAt.Time,
	}
	_ = json.Unmarshal([]byte(r.Plan), &s.Plan)
	return s
}

// Syncs reads a router's history, newest first, below id before (0: newest).
func (s *Service) Syncs(ctx context.Context, id, before int64, limit int) ([]Sync, error) {
	rows, err := store.Syncs(ctx, s.d.DB.R, id, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Sync, 0, len(rows))
	for _, r := range rows {
		out = append(out, syncOf(r))
	}
	return out, nil
}

// SyncRecord reads one row of a router.
func (s *Service) SyncRecord(ctx context.Context, id, syncID int64) (Sync, error) {
	r, err := store.GetSync(ctx, s.d.DB.R, syncID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && r.RouterID != id) {
		return Sync{}, ErrNotFound
	}
	return syncOf(r), err
}

// LatestPreview is the router's newest preview of the last hour.
func (s *Service) LatestPreview(ctx context.Context, id int64) (Sync, bool, error) {
	r, err := store.LatestSync(ctx, s.d.DB.R, id, "preview")
	if errors.Is(err, store.ErrNotFound) {
		return Sync{}, false, nil
	}
	if err != nil {
		return Sync{}, false, err
	}
	if s.d.Now().Sub(r.StartedAt.Time) > PreviewShown {
		return Sync{}, false, nil
	}
	return syncOf(r), true, nil
}

// LatestPlan is the newest plan read from the router (a sync's or a
// preview's): the Tags area's.
func (s *Service) LatestPlan(ctx context.Context, id int64) (Sync, bool, error) {
	r, err := store.LatestPlanned(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Sync{}, false, nil
	}
	return syncOf(r), err == nil, err
}

// Applied reads what Proxier last installed on the router, by tag.
func (s *Service) Applied(ctx context.Context, id int64) (map[string]Applied, error) {
	rows, err := store.RouterTags(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Applied, len(rows))
	for _, r := range rows {
		out[r.Tag] = Applied{Hash: r.Hash, Suffix: r.Suffix, Exact: r.Exact, At: r.AppliedAt.Time}
	}
	return out, nil
}

// Prune deletes old sync history (the newest rows of each router kept) and
// old Test connection runs.
func (s *Service) Prune(ctx context.Context) (syncs, tests int64, err error) {
	now := s.d.Now()
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		syncs, tests, err = store.PruneSyncs(ctx, tx, db.At(now.Add(-store.SyncRetention)), store.SyncKeep, db.At(now.Add(-store.TestRetention)))
		return err
	})
	return syncs, tests, err
}

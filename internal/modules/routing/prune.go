package routing

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// JobPrune is the module's retention job, and its schedule.
const JobPrune = "routing.prune"

// pruneType deletes what the module keeps only for a while
// (docs/data-model.md#retention). It records nothing. Later sub-phases add
// their own lines (fetch logs, syncs, discovery runs).
func (m *Module) pruneType() jobs.Type {
	return jobs.Type{Name: JobPrune, Queue: jobs.Maintenance, MaxAttempts: 1, Steps: []jobs.Step{{
		Name: "prune", Run: func(ctx context.Context, r *jobs.Run) error { return m.Prune(ctx, r.Log()) },
	}}}
}

func (m *Module) pruneSchedule() jobs.Schedule {
	return jobs.Schedule{Name: JobPrune, At: "05:10", Request: func(context.Context) (jobs.Request, error) {
		return jobs.Request{Type: JobPrune, CoalescingKey: "prune"}, nil
	}}
}

// Prune deletes old superseded snapshots and settled rejected ones,
// Shadowrocket fetches after 90 days, imports after 30 days, router syncs
// after 90 days (the newest 20 per router kept), connection tests after a
// day, discovery runs and their screenshots after 30 days, and catalog rows of generations not in force, in batches. log may be
// nil.
func (m *Module) Prune(ctx context.Context, log *jobs.Logger) error {
	cutoff := db.At(m.Now().Add(-store.SnapshotRetention))
	var total int64
	for {
		var n int64
		if err := m.deps.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			n, err = store.PruneSnapshots(ctx, tx, cutoff, store.PruneBatch)
			return err
		}); err != nil {
			return err
		}
		if total += n; n == 0 {
			break
		}
	}
	if log != nil {
		log.Info("%d snapshots deleted", total)
	}
	fetches, err := m.Shadowrocket.Prune(ctx)
	if err != nil {
		return err
	}
	cutoff = db.At(m.Now().Add(-store.ImportRetention))
	var imports int64
	for {
		var n int64
		if err := m.deps.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			n, err = store.PruneImports(ctx, tx, cutoff, store.PruneBatch)
			return err
		}); err != nil {
			return err
		}
		if imports += n; n == 0 {
			break
		}
	}
	if log != nil {
		log.Info("%d Shadowrocket fetches and %d imports deleted", fetches, imports)
	}
	syncs, tests, err := m.Routers.Prune(ctx)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("%d router syncs and %d connection tests deleted", syncs, tests)
	}
	runs, err := m.Discovery.Prune(ctx)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("%d discovery runs deleted", runs)
	}
	return m.Catalog.Prune(ctx)
}

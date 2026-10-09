package store

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Retention of sync history: rows older than this go, the newest
// SyncKeep per router stay; Test connection rows go after TestRetention.
const (
	SyncRetention = 90 * 24 * time.Hour
	SyncKeep      = 20
	TestRetention = 24 * time.Hour
)

// Sync is a row of routing_syncs.
type Sync struct {
	ID         int64   `db:"id"`
	RouterID   int64   `db:"router_id"`
	JobID      int64   `db:"job_id"`
	Kind       string  `db:"kind"`
	Trigger    string  `db:"trigger"`
	State      string  `db:"state"`
	Step       string  `db:"step"`
	Hop        string  `db:"hop"`
	Error      string  `db:"error"`
	ReadAt     db.Time `db:"read_at"`
	Plan       string  `db:"plan"`
	Script     string  `db:"script"`
	Added      int     `db:"added"`
	Updated    int     `db:"updated"`
	Removed    int     `db:"removed"`
	Unchanged  int     `db:"unchanged"`
	Recorded   int     `db:"recorded"`
	Drift      string  `db:"drift"`
	StartedAt  db.Time `db:"started_at"`
	FinishedAt db.Time `db:"finished_at"`
}

const syncSelect = `SELECT id, router_id, coalesce(job_id, 0) AS job_id, kind, "trigger", state, step, hop, error, read_at, plan, script,
	added, updated, removed, unchanged, recorded, drift, started_at, finished_at FROM routing_syncs`

// InsertSync starts a sync row in state running.
func InsertSync(ctx context.Context, x sqlx.ExtContext, s Sync) (int64, error) {
	var job any
	if s.JobID != 0 {
		job = s.JobID
	}
	res, err := x.ExecContext(ctx, `INSERT INTO routing_syncs (router_id, job_id, kind, "trigger", state, started_at) VALUES (?, ?, ?, ?, 'running', ?)`,
		s.RouterID, job, s.Kind, s.Trigger, s.StartedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetSyncJob ties a sync row to its job.
func SetSyncJob(ctx context.Context, x sqlx.ExtContext, id, jobID int64) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_syncs SET job_id = ? WHERE id = ?`, jobID, id)
	return err
}

// GetSync reads one sync row.
func GetSync(ctx context.Context, q sqlx.QueryerContext, id int64) (Sync, error) {
	var s Sync
	err := sqlx.GetContext(ctx, q, &s, syncSelect+` WHERE id = ?`, id)
	return s, notFound(err)
}

// RunningSyncOfJob is the running row of a job and kind; ErrNotFound when none.
func RunningSyncOfJob(ctx context.Context, q sqlx.QueryerContext, jobID int64, kind string) (Sync, error) {
	var s Sync
	err := sqlx.GetContext(ctx, q, &s, syncSelect+` WHERE job_id = ? AND kind = ? AND state = 'running' ORDER BY id DESC LIMIT 1`, jobID, kind)
	return s, notFound(err)
}

// Syncs reads a router's rows, newest first, below id before (0: from the newest).
func Syncs(ctx context.Context, q sqlx.QueryerContext, routerID, before int64, limit int) ([]Sync, error) {
	var out []Sync
	if before <= 0 {
		before = 1 << 62
	}
	err := sqlx.SelectContext(ctx, q, &out, syncSelect+` WHERE router_id = ? AND id < ? ORDER BY id DESC LIMIT ?`, routerID, before, limit)
	return out, err
}

// LatestSync is a router's newest row of a kind; ErrNotFound when none.
func LatestSync(ctx context.Context, q sqlx.QueryerContext, routerID int64, kind string) (Sync, error) {
	var s Sync
	err := sqlx.GetContext(ctx, q, &s, syncSelect+` WHERE router_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, routerID, kind)
	return s, notFound(err)
}

// LatestPlanned is a router's newest finished sync or preview that read the
// router (its plan is the Tags area's); ErrNotFound when none.
func LatestPlanned(ctx context.Context, q sqlx.QueryerContext, routerID int64) (Sync, error) {
	var s Sync
	err := sqlx.GetContext(ctx, q, &s, syncSelect+` WHERE router_id = ? AND kind IN ('sync','preview') AND read_at IS NOT NULL
		ORDER BY read_at DESC, id DESC LIMIT 1`, routerID)
	return s, notFound(err)
}

// SyncPlanned stores what a sync read and planned.
func SyncPlanned(ctx context.Context, x sqlx.ExtContext, id int64, plan string, at db.Time, s Sync) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_syncs SET plan = ?, read_at = ?, added = ?, updated = ?, removed = ?, unchanged = ?,
		recorded = ? WHERE id = ?`, plan, at, s.Added, s.Updated, s.Removed, s.Unchanged, s.Recorded, id)
	return err
}

// SyncDone ends a row; script only for previews.
func SyncDone(ctx context.Context, x sqlx.ExtContext, id int64, script string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_syncs SET state = 'done', script = ?, finished_at = ? WHERE id = ?`, script, at, id)
	return err
}

// SyncFailed ends a row failed at a step (and a hop, for connect).
func SyncFailed(ctx context.Context, x sqlx.ExtContext, id int64, step, hop, text string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_syncs SET state = 'failed', step = ?, hop = ?, error = ?, finished_at = ? WHERE id = ? AND state = 'running'`,
		step, hop, text, at, id)
	return err
}

// SyncsOfJobCancelled ends a job's running rows as cancelled.
func SyncsOfJobCancelled(ctx context.Context, x sqlx.ExtContext, jobID int64, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_syncs SET state = 'cancelled', finished_at = ? WHERE job_id = ? AND state = 'running'`, at, jobID)
	return err
}

// SyncsOfJobFailed ends a job's running rows as failed, when nothing else did.
func SyncsOfJobFailed(ctx context.Context, x sqlx.ExtContext, jobID int64, text string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_syncs SET state = 'failed', error = ?, finished_at = ? WHERE job_id = ? AND state = 'running'`,
		text, at, jobID)
	return err
}

// DeleteSync removes a row (a preview merged into one already queued).
func DeleteSync(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_syncs WHERE id = ?`, id)
	return err
}

// RunningPreview is a router's newest running preview row; ErrNotFound when none.
func RunningPreview(ctx context.Context, q sqlx.QueryerContext, routerID int64, except int64) (Sync, error) {
	var s Sync
	err := sqlx.GetContext(ctx, q, &s, syncSelect+` WHERE router_id = ? AND kind = 'preview' AND state = 'running' AND id <> ?
		ORDER BY id DESC LIMIT 1`, routerID, except)
	return s, notFound(err)
}

// PruneSyncs deletes rows that ended before cutoff, keeping each router's
// newest keep rows, and Test connection rows created before testCutoff.
func PruneSyncs(ctx context.Context, x sqlx.ExtContext, cutoff db.Time, keep int, testCutoff db.Time) (syncs, tests int64, err error) {
	res, err := x.ExecContext(ctx, `DELETE FROM routing_syncs WHERE state <> 'running' AND started_at < ? AND id NOT IN (
		SELECT id FROM (SELECT id, row_number() OVER (PARTITION BY router_id ORDER BY id DESC) AS n FROM routing_syncs) WHERE n <= ?)`,
		cutoff, keep)
	if err != nil {
		return 0, 0, err
	}
	syncs, _ = res.RowsAffected()
	res, err = x.ExecContext(ctx, `DELETE FROM routing_tests WHERE state <> 'running' AND created_at < ?`, testCutoff)
	if err != nil {
		return syncs, 0, err
	}
	tests, _ = res.RowsAffected()
	return syncs, tests, nil
}

package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Snapshot is a row of routing_snapshots. Suffix and Exact are the stored
// text (snapshot.Encode); Skipped is JSON.
type Snapshot struct {
	ID          int64   `db:"id"`
	ServiceID   int64   `db:"service_id"`
	Status      string  `db:"status"`
	Selector    string  `db:"selector"`
	Portal      string  `db:"portal"`
	Kind        string  `db:"kind"`
	Suffix      string  `db:"suffix"`
	Exact       string  `db:"exact"`
	Skipped     string  `db:"skipped"`
	SuffixCount int     `db:"suffix_count"`
	ExactCount  int     `db:"exact_count"`
	Hash        string  `db:"hash"`
	Added       int     `db:"added"`
	Removed     int     `db:"removed"`
	Reason      string  `db:"reason"`
	LostPct     int     `db:"lost_pct"`
	ForcedBy    string  `db:"forced_by"`
	InRound     bool    `db:"in_round"`
	DismissedAt db.Time `db:"dismissed_at"`
	FetchedAt   db.Time `db:"fetched_at"`
	AcceptedAt  db.Time `db:"accepted_at"`
}

const snapshotCols = `id, service_id, status, selector, portal, kind, suffix, exact, skipped, suffix_count, exact_count, hash,
	added, removed, reason, lost_pct, forced_by, in_round, dismissed_at, fetched_at, accepted_at`

// The history leaves the names out: they can be large.
const historyCols = `id, service_id, status, selector, portal, kind, '' AS suffix, '' AS exact, '[]' AS skipped, suffix_count,
	exact_count, hash, added, removed, reason, lost_pct, forced_by, in_round, dismissed_at, fetched_at, accepted_at`

// InsertSnapshot adds a snapshot as it is (a rejected one, or the first
// accepted one of a new service).
func InsertSnapshot(ctx context.Context, x sqlx.ExtContext, s Snapshot) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO routing_snapshots (service_id, status, selector, portal, kind, suffix, exact,
			skipped, suffix_count, exact_count, hash, added, removed, reason, lost_pct, forced_by, in_round, dismissed_at,
			fetched_at, accepted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ServiceID, s.Status, s.Selector, s.Portal, s.Kind, s.Suffix, s.Exact, s.Skipped, s.SuffixCount, s.ExactCount,
		s.Hash, s.Added, s.Removed, s.Reason, s.LostPct, s.ForcedBy, s.InRound, s.DismissedAt, s.FetchedAt, s.AcceptedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Accept makes s the service's accepted snapshot: the one in force becomes
// superseded first (the partial unique index allows one accepted snapshot).
func Accept(ctx context.Context, x sqlx.ExtContext, s Snapshot) (int64, error) {
	if _, err := x.ExecContext(ctx, `UPDATE routing_snapshots SET status = 'superseded' WHERE service_id = ? AND status = 'accepted'`,
		s.ServiceID); err != nil {
		return 0, err
	}
	s.Status = "accepted"
	return InsertSnapshot(ctx, x, s)
}

// AcceptedSnapshot reads a service's accepted snapshot with its names.
func AcceptedSnapshot(ctx context.Context, q sqlx.QueryerContext, serviceID int64) (Snapshot, error) {
	var s Snapshot
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+snapshotCols+` FROM routing_snapshots WHERE service_id = ? AND status = 'accepted'`, serviceID)
	return s, notFound(err)
}

// GetSnapshot reads one snapshot of a service with its names.
func GetSnapshot(ctx context.Context, q sqlx.QueryerContext, serviceID, id int64) (Snapshot, error) {
	var s Snapshot
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+snapshotCols+` FROM routing_snapshots WHERE service_id = ? AND id = ?`, serviceID, id)
	return s, notFound(err)
}

// AcceptedBefore reads the snapshot that was accepted last before id (it may
// have been superseded since); ErrNotFound for the first one.
func AcceptedBefore(ctx context.Context, q sqlx.QueryerContext, serviceID, id int64) (Snapshot, error) {
	var s Snapshot
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+snapshotCols+` FROM routing_snapshots
		WHERE service_id = ? AND id < ? AND accepted_at IS NOT NULL ORDER BY id DESC LIMIT 1`, serviceID, id)
	return s, notFound(err)
}

// SnapshotHistory lists a service's snapshots without their names, newest first.
func SnapshotHistory(ctx context.Context, q sqlx.QueryerContext, serviceID int64) ([]Snapshot, error) {
	var out []Snapshot
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+historyCols+` FROM routing_snapshots WHERE service_id = ? ORDER BY id DESC`, serviceID)
	return out, err
}

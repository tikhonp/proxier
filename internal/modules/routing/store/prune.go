package store

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Retention (docs/data-model.md#retention).
const (
	// SnapshotRetention keeps superseded and settled rejected snapshots for
	// diffs. The accepted one and a waiting one are kept for as long as the
	// service exists.
	SnapshotRetention = 90 * 24 * time.Hour
	// PruneBatch is how many rows one prune transaction deletes.
	PruneBatch = 5000
)

// PruneSnapshots deletes at most limit snapshots fetched before cutoff that
// are superseded, or rejected and no longer waiting (dismissed, or older
// than the accepted one).
func PruneSnapshots(ctx context.Context, x sqlx.ExtContext, cutoff db.Time, limit int) (int64, error) {
	res, err := x.ExecContext(ctx, `DELETE FROM routing_snapshots WHERE id IN (
		SELECT r.id FROM routing_snapshots r
		WHERE r.fetched_at < ? AND (r.status = 'superseded' OR r.status = 'rejected' AND (
			r.dismissed_at IS NOT NULL
			OR r.id < coalesce((SELECT a.id FROM routing_snapshots a WHERE a.service_id = r.service_id AND a.status = 'accepted'), 0)
			OR r.id <> (SELECT max(n.id) FROM routing_snapshots n WHERE n.service_id = r.service_id AND n.status = 'rejected')))
		LIMIT ?)`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

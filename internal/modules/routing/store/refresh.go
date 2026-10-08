package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// The states of a service as the refresh leaves it (3c).
const (
	StateOK      = "ok"
	StateWaiting = "waiting" // a rejected snapshot waits for Accept anyway or Dismiss
	StateFailing = "failing" // the last refresh failed
)

// UpstreamInLists reads the upstream services that at least one list holds,
// by id: what the daily round refreshes.
func UpstreamInLists(ctx context.Context, q sqlx.QueryerContext) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT s.id FROM routing_services s
		WHERE s.source <> 'custom' AND EXISTS (SELECT 1 FROM routing_list_services m WHERE m.service_id = s.id)
		ORDER BY s.id`)
	return out, err
}

// UpstreamMembers reads a list's upstream services, in list order.
func UpstreamMembers(ctx context.Context, q sqlx.QueryerContext, listID int64) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT m.service_id FROM routing_list_services m
		JOIN routing_services s ON s.id = m.service_id
		WHERE m.list_id = ? AND s.source <> 'custom' ORDER BY m.position`, listID)
	return out, err
}

// RefreshFailed counts a failed refresh and returns the service as it is now.
func RefreshFailed(ctx context.Context, x sqlx.ExtContext, id int64, errText string) (Service, error) {
	if _, err := x.ExecContext(ctx, `UPDATE routing_services SET failures = failures + 1, last_error = ? WHERE id = ?`,
		errText, id); err != nil {
		return Service{}, err
	}
	return GetService(ctx, x, id)
}

// SetFailingNotified records that refresh_failing was sent for this run of failures.
func SetFailingNotified(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_services SET failing_notified = 1 WHERE id = ?`, id)
	return err
}

// RefreshAnswered is the bookkeeping of a refresh that got an answer.
func RefreshAnswered(ctx context.Context, x sqlx.ExtContext, id int64, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_services SET last_checked_at = ?, failures = 0, last_error = '', failing_notified = 0
		WHERE id = ?`, at, id)
	return err
}

// Waiting reads a service's waiting snapshot with its names: the newest
// rejected snapshot fetched after the accepted one, when it is undismissed.
// ok is false when nothing waits.
func Waiting(ctx context.Context, q sqlx.QueryerContext, serviceID int64) (s Snapshot, ok bool, err error) {
	err = sqlx.GetContext(ctx, q, &s, `SELECT `+snapshotCols+` FROM routing_snapshots r
		WHERE r.service_id = ? AND r.status = 'rejected'
		  AND r.id > coalesce((SELECT a.id FROM routing_snapshots a WHERE a.service_id = r.service_id AND a.status = 'accepted'), 0)
		ORDER BY r.id DESC LIMIT 1`, serviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	return s, s.DismissedAt.IsZero(), nil
}

// DismissSnapshot ends a waiting snapshot.
func DismissSnapshot(ctx context.Context, x sqlx.ExtContext, id int64, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_snapshots SET dismissed_at = ? WHERE id = ? AND status = 'rejected'`, at, id)
	return err
}

// AcceptRejected makes a rejected snapshot the accepted one (Accept anyway):
// the one in force becomes superseded first.
func AcceptRejected(ctx context.Context, x sqlx.ExtContext, serviceID, id int64, forcedBy string, added, removed int, at db.Time) error {
	if _, err := x.ExecContext(ctx, `UPDATE routing_snapshots SET status = 'superseded' WHERE service_id = ? AND status = 'accepted'`,
		serviceID); err != nil {
		return err
	}
	_, err := x.ExecContext(ctx, `UPDATE routing_snapshots SET status = 'accepted', forced_by = ?, accepted_at = ?, added = ?, removed = ?
		WHERE id = ? AND service_id = ?`, forcedBy, at, added, removed, id, serviceID)
	return err
}

// States reads every service's state: waiting while a snapshot waits, else
// failing while its last refresh failed, else ok.
func States(ctx context.Context, q sqlx.QueryerContext) (map[int64]string, error) {
	var rows []struct {
		ID       int64 `db:"id"`
		Failures int   `db:"failures"`
		Waiting  bool  `db:"waiting"`
	}
	err := sqlx.SelectContext(ctx, q, &rows, `
		SELECT s.id, s.failures, coalesce((
			SELECT r.dismissed_at IS NULL FROM routing_snapshots r
			WHERE r.service_id = s.id AND r.status = 'rejected'
			  AND r.id > coalesce((SELECT a.id FROM routing_snapshots a WHERE a.service_id = s.id AND a.status = 'accepted'), 0)
			ORDER BY r.id DESC LIMIT 1), 0) AS waiting
		FROM routing_services s`)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		switch {
		case r.Waiting:
			out[r.ID] = StateWaiting
		case r.Failures > 0:
			out[r.ID] = StateFailing
		default:
			out[r.ID] = StateOK
		}
	}
	return out, nil
}

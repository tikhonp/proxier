package store

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// CutOff is a row of subs_cutoffs.
type CutOff struct {
	ID        int64   `db:"id"`
	LinkID    int64   `db:"link_id"`
	CreatedAt db.Time `db:"created_at"`
	CreatedBy string  `db:"created_by"`
}

// CutOffItem is one server of a cut-off.
type CutOffItem struct {
	CutOffID   int64         `db:"cutoff_id"`
	Position   int           `db:"position"`
	ServerID   int64         `db:"server_id"`
	ServerName string        `db:"server_name"`
	State      string        `db:"state"`
	JobID      sql.NullInt64 `db:"job_id"`
	Error      string        `db:"error"`
}

const cutOffItemCols = `cutoff_id, position, server_id, server_name, state, job_id, error`

// InsertCutOff starts a cut-off of the link.
func InsertCutOff(ctx context.Context, x sqlx.ExtContext, linkID int64, at db.Time, by string) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO subs_cutoffs (link_id, created_at, created_by) VALUES (?, ?, ?)`, linkID, at, by)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertCutOffItem adds a server to a cut-off; errText is the reason of a
// skipped one.
func InsertCutOffItem(ctx context.Context, x sqlx.ExtContext, it CutOffItem) error {
	_, err := x.ExecContext(ctx, `INSERT INTO subs_cutoff_items (cutoff_id, position, server_id, server_name, state, error)
		VALUES (?, ?, ?, ?, ?, ?)`, it.CutOffID, it.Position, it.ServerID, it.ServerName, it.State, it.Error)
	return err
}

// LatestCutOff is the newest cut-off of the link.
func LatestCutOff(ctx context.Context, q sqlx.QueryerContext, linkID int64) (CutOff, error) {
	var c CutOff
	err := sqlx.GetContext(ctx, q, &c, `SELECT id, link_id, created_at, created_by FROM subs_cutoffs
		WHERE link_id = ? ORDER BY id DESC LIMIT 1`, linkID)
	return c, notFound(err)
}

// CutOffItems of a cut-off, by position.
func CutOffItems(ctx context.Context, q sqlx.QueryerContext, cutoffID int64) ([]CutOffItem, error) {
	var out []CutOffItem
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+cutOffItemCols+` FROM subs_cutoff_items
		WHERE cutoff_id = ? ORDER BY position`, cutoffID)
	return out, err
}

// RunningCutOffItem is the item whose rotation is the job, while it runs.
func RunningCutOffItem(ctx context.Context, q sqlx.QueryerContext, jobID int64) (CutOffItem, error) {
	var it CutOffItem
	err := sqlx.GetContext(ctx, q, &it, `SELECT `+cutOffItemCols+` FROM subs_cutoff_items
		WHERE job_id = ? AND state = 'running' LIMIT 1`, jobID)
	return it, notFound(err)
}

// SetCutOffItem moves an item to a state; jobID 0 clears the job.
func SetCutOffItem(ctx context.Context, x sqlx.ExtContext, cutoffID int64, position int, state string, jobID int64, errText string) error {
	job := sql.NullInt64{Int64: jobID, Valid: jobID != 0}
	_, err := x.ExecContext(ctx, `UPDATE subs_cutoff_items SET state = ?, job_id = ?, error = ? WHERE cutoff_id = ? AND position = ?`,
		state, job, errText, cutoffID, position)
	return err
}

// OtherActiveLinks counts the active, unexpired links other than except whose
// subscription holds any of the servers, and those subscriptions.
func OtherActiveLinks(ctx context.Context, q sqlx.QueryerContext, serverIDs []int64, except int64, now db.Time) (links, subs int, err error) {
	if len(serverIDs) == 0 {
		return 0, 0, nil
	}
	query, args, err := sqlx.In(`SELECT count(*) AS links, count(DISTINCT subscription_id) AS subs FROM subs_links
		WHERE id <> ? AND state = 'active' AND (expires_at IS NULL OR expires_at > ?)
		AND subscription_id IN (SELECT subscription_id FROM subs_subscription_servers WHERE server_id IN (?))`, except, now, serverIDs)
	if err != nil {
		return 0, 0, err
	}
	var r struct {
		Links int `db:"links"`
		Subs  int `db:"subs"`
	}
	err = sqlx.GetContext(ctx, q, &r, query, args...)
	return r.Links, r.Subs, err
}

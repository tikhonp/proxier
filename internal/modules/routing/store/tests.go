package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Test is a row of routing_tests.
type Test struct {
	ID             int64   `db:"id"`
	RouterID       int64   `db:"router_id"` // 0: the add form
	Form           string  `db:"form"`
	JobID          int64   `db:"job_id"`
	State          string  `db:"state"`
	Checks         string  `db:"checks"`
	ConfirmHop     string  `db:"confirm_hop"`
	ConfirmAddress string  `db:"confirm_address"`
	ConfirmKey     string  `db:"confirm_key"`
	ConfirmFP      string  `db:"confirm_fp"`
	Version        string  `db:"version"`
	Board          string  `db:"board"`
	Error          string  `db:"error"`
	CreatedAt      db.Time `db:"created_at"`
	FinishedAt     db.Time `db:"finished_at"`
}

const testSelect = `SELECT id, coalesce(router_id, 0) AS router_id, form, coalesce(job_id, 0) AS job_id, state, checks, confirm_hop,
	confirm_address, confirm_key, confirm_fp, version, board, error, created_at, finished_at FROM routing_tests`

// InsertTest starts a test in state running.
func InsertTest(ctx context.Context, x sqlx.ExtContext, routerID int64, form string, at db.Time) (int64, error) {
	var rid any
	if routerID != 0 {
		rid = routerID
	}
	res, err := x.ExecContext(ctx, `INSERT INTO routing_tests (router_id, form, state, created_at) VALUES (?, ?, 'running', ?)`, rid, form, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetTestJob ties a test to its job.
func SetTestJob(ctx context.Context, x sqlx.ExtContext, id, jobID int64) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_tests SET job_id = ? WHERE id = ?`, jobID, id)
	return err
}

// GetTest reads one test.
func GetTest(ctx context.Context, q sqlx.QueryerContext, id int64) (Test, error) {
	var t Test
	err := sqlx.GetContext(ctx, q, &t, testSelect+` WHERE id = ?`, id)
	return t, notFound(err)
}

// LatestTest is a router's newest test; ErrNotFound when none.
func LatestTest(ctx context.Context, q sqlx.QueryerContext, routerID int64) (Test, error) {
	var t Test
	err := sqlx.GetContext(ctx, q, &t, testSelect+` WHERE router_id = ? ORDER BY id DESC LIMIT 1`, routerID)
	return t, notFound(err)
}

// FinishTest ends a test: passed, warned or failed with its checks.
func FinishTest(ctx context.Context, x sqlx.ExtContext, t Test) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_tests SET state = ?, checks = ?, version = ?, board = ?, error = ?, finished_at = ?,
		confirm_hop = '', confirm_address = '', confirm_key = '', confirm_fp = '' WHERE id = ?`,
		t.State, t.Checks, t.Version, t.Board, t.Error, t.FinishedAt, t.ID)
	return err
}

// TestNeedsConfirm stops a test at a host key to confirm.
func TestNeedsConfirm(ctx context.Context, x sqlx.ExtContext, t Test) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_tests SET state = 'confirm', confirm_hop = ?, confirm_address = ?, confirm_key = ?,
		confirm_fp = ?, finished_at = ? WHERE id = ?`, t.ConfirmHop, t.ConfirmAddress, t.ConfirmKey, t.ConfirmFP, t.FinishedAt, t.ID)
	return err
}

// TestsOfJobFailed ends a job's running test as failed.
func TestsOfJobFailed(ctx context.Context, x sqlx.ExtContext, jobID int64, text string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_tests SET state = 'failed', error = ?, finished_at = ? WHERE job_id = ? AND state = 'running'`,
		text, at, jobID)
	return err
}

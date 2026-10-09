package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// ImportRetention is how long an mtvpn import's preview and result are kept.
const ImportRetention = 30 * 24 * time.Hour

// Import is a row of routing_imports. Rows, Shadowrocket and ListFailures
// are JSON the mtvpn package reads; the file's text is never here.
type Import struct {
	ID           int64         `db:"id"`
	State        string        `db:"state"`
	ListID       sql.NullInt64 `db:"list_id"`
	Rows         string        `db:"rows"`
	Ignored      string        `db:"ignored"`
	Base         string        `db:"base"`
	BaseURL      string        `db:"base_url"`
	Shadowrocket string        `db:"shadowrocket"`
	ListFailures string        `db:"list_failures"`
	JobID        sql.NullInt64 `db:"job_id"`
	CreatedAt    db.Time       `db:"created_at"`
	FinishedAt   db.Time       `db:"finished_at"`
}

const importCols = `id, state, list_id, rows, ignored, base, base_url, shadowrocket, list_failures, job_id, created_at, finished_at`

// InsertImport stores a preview and returns its id.
func InsertImport(ctx context.Context, x sqlx.ExtContext, im Import) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO routing_imports (state, list_id, rows, ignored, base, base_url, shadowrocket, list_failures, created_at)
		VALUES ('preview', ?, ?, ?, ?, ?, ?, ?, ?)`,
		im.ListID, im.Rows, im.Ignored, im.Base, im.BaseURL, im.Shadowrocket, im.ListFailures, im.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetImport reads one import.
func GetImport(ctx context.Context, q sqlx.QueryerContext, id int64) (Import, error) {
	var im Import
	err := sqlx.GetContext(ctx, q, &im, `SELECT `+importCols+` FROM routing_imports WHERE id = ?`, id)
	return im, notFound(err)
}

// StartImport saves the admin's choices and the job, and marks the import running.
func StartImport(ctx context.Context, x sqlx.ExtContext, id, listID int64, rows, shadowrocket string, jobID int64) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_imports SET state = 'running', list_id = ?, rows = ?, shadowrocket = ?, job_id = ? WHERE id = ?`,
		listID, rows, shadowrocket, jobID, id)
	return err
}

// SaveImportRows writes the rows with their outcomes so far.
func SaveImportRows(ctx context.Context, x sqlx.ExtContext, id int64, rows string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_imports SET rows = ? WHERE id = ?`, rows, id)
	return err
}

// SaveImportShadowrocket writes the config's outcome.
func SaveImportShadowrocket(ctx context.Context, x sqlx.ExtContext, id int64, shadowrocket string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_imports SET shadowrocket = ? WHERE id = ?`, shadowrocket, id)
	return err
}

// FinishImport ends a running import as done or failed.
func FinishImport(ctx context.Context, x sqlx.ExtContext, id int64, state string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_imports SET state = ?, finished_at = ? WHERE id = ? AND state = 'running'`, state, at, id)
	return err
}

// FailImportOfJob ends the running import of a job as failed.
func FailImportOfJob(ctx context.Context, x sqlx.ExtContext, jobID int64, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_imports SET state = 'failed', finished_at = ? WHERE job_id = ? AND state = 'running'`, at, jobID)
	return err
}

// PruneImports deletes at most limit imports created before cutoff that
// aren't running.
func PruneImports(ctx context.Context, x sqlx.ExtContext, cutoff db.Time, limit int) (int64, error) {
	res, err := x.ExecContext(ctx, `DELETE FROM routing_imports WHERE id IN (
		SELECT id FROM routing_imports WHERE created_at < ? AND state <> 'running' LIMIT ?)`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

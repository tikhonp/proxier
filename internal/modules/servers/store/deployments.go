package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// GetDeployment returns one deployment.
func GetDeployment(ctx context.Context, q sqlx.QueryerContext, id int64) (Deployment, error) {
	var d Deployment
	return d, notFound(sqlx.GetContext(ctx, q, &d, deploymentSelect+` WHERE id = ?`, id))
}

// Deployments returns a server's deployments, newest first.
func Deployments(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]Deployment, error) {
	var out []Deployment
	err := sqlx.SelectContext(ctx, q, &out, deploymentSelect+` WHERE server_id = ? ORDER BY id DESC`, serverID)
	return out, err
}

// LatestDeployment is the server's newest deployment of any kind.
func LatestDeployment(ctx context.Context, q sqlx.QueryerContext, serverID int64) (Deployment, bool, error) {
	var d Deployment
	err := sqlx.GetContext(ctx, q, &d, deploymentSelect+` WHERE server_id = ? ORDER BY id DESC LIMIT 1`, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, nil
	}
	return d, err == nil, err
}

// PreviousUploading is the newest succeeded deployment before id that
// uploaded files: what the history's Diff compares with.
func PreviousUploading(ctx context.Context, q sqlx.QueryerContext, serverID, id int64) (Deployment, bool, error) {
	var d Deployment
	err := sqlx.GetContext(ctx, q, &d, deploymentSelect+` WHERE server_id = ? AND id < ? AND state = 'succeeded' AND uploaded = 1 ORDER BY id DESC LIMIT 1`, serverID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, nil
	}
	return d, err == nil, err
}

// UploadedAfter lists the server's deployments newer than id that put files
// on it, whatever their state: what a restore has to clean up after.
func UploadedAfter(ctx context.Context, q sqlx.QueryerContext, serverID, id int64) ([]Deployment, error) {
	var out []Deployment
	err := sqlx.SelectContext(ctx, q, &out, deploymentSelect+` WHERE server_id = ? AND id > ? AND uploaded = 1 AND kind <> 'restore' ORDER BY id`, serverID, id)
	return out, err
}

// HasUploadedDeployment reports whether any deployment of the server put
// files on it, even one that failed: a failed server with a stack has logs.
func HasUploadedDeployment(ctx context.Context, q sqlx.QueryerContext, serverID int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM servers_deployments WHERE server_id = ? AND uploaded = 1`, serverID)
	return n > 0, err
}

// ReopenDeploymentByID takes a failed deployment back to running for a job
// that carries on (the job page's Retry).
func ReopenDeploymentByID(ctx context.Context, tx sqlx.ExtContext, id, jobID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET state = 'running', job_id = ?, error = NULL, finished_at = NULL
		WHERE id = ? AND state = 'failed'`, jobID, id)
	return err
}

// SetDeploymentFilesChanged records how many files a deployment changed.
func SetDeploymentFilesChanged(ctx context.Context, tx sqlx.ExtContext, id int64, n int) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET files_changed = ? WHERE id = ?`, n, id)
	return err
}

// SetServerParams stores the template version and parameters a deployment
// put in force.
func SetServerParams(ctx context.Context, tx sqlx.ExtContext, id int64, version int, params string, paramsSecret []byte) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET template_version = ?, params = ?, params_secret = ? WHERE id = ? AND state = 'active'`,
		version, params, paramsSecret, id)
	return err
}

// --- generated values in a rotation

// SetPending stores a rotation's new value next to the old one, unless a
// pending value exists already; it reports whether the value was stored.
func SetPending(ctx context.Context, tx sqlx.ExtContext, serverID int64, key string, sealed []byte) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE servers_generated_values SET pending = ? WHERE server_id = ? AND key = ? AND pending IS NULL`,
		sealed, serverID, key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CommitPending makes every pending value the value and returns the keys.
func CommitPending(ctx context.Context, tx sqlx.ExtContext, serverID int64, at db.Time) ([]string, error) {
	var keys []string
	if err := sqlx.SelectContext(ctx, tx, &keys, `SELECT key FROM servers_generated_values WHERE server_id = ? AND pending IS NOT NULL ORDER BY key`, serverID); err != nil {
		return nil, err
	}
	_, err := tx.ExecContext(ctx, `UPDATE servers_generated_values SET value = pending, pending = NULL, rotated_at = ?
		WHERE server_id = ? AND pending IS NOT NULL`, at, serverID)
	return keys, err
}

// ClearPending drops every pending value: the old ones stay in force.
func ClearPending(ctx context.Context, tx sqlx.ExtContext, serverID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_generated_values SET pending = NULL WHERE server_id = ? AND pending IS NOT NULL`, serverID)
	return err
}

// --- rollouts

// Rollout is a rolling upgrade.
type Rollout struct {
	ID         int64   `db:"id"`
	TemplateID int64   `db:"template_id"`
	ToVersion  int     `db:"to_version"`
	State      string  `db:"state"` // running done stopped cancelled
	CreatedAt  db.Time `db:"created_at"`
	CreatedBy  string  `db:"created_by"`
	FinishedAt db.Time `db:"finished_at"`
}

// RolloutItem is one server of a rollout.
type RolloutItem struct {
	RolloutID    int64         `db:"rollout_id"`
	Position     int           `db:"position"`
	ServerID     int64         `db:"server_id"`
	FromVersion  int           `db:"from_version"`
	Params       string        `db:"params"`
	ParamsSecret []byte        `db:"params_secret"`
	State        string        `db:"state"` // waiting running done failed skipped
	JobID        sql.NullInt64 `db:"job_id"`
	Error        string        `db:"error"`
}

const rolloutSelect = `SELECT id, template_id, to_version, state, created_at, created_by, finished_at FROM servers_rollouts`
const itemSelect = `SELECT rollout_id, position, server_id, from_version, params, params_secret, state, job_id, COALESCE(error, '') AS error FROM servers_rollout_items`

// InsertRollout adds a running rollout.
func InsertRollout(ctx context.Context, tx sqlx.ExtContext, templateID int64, toVersion int, by string, at db.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO servers_rollouts (template_id, to_version, state, created_at, created_by) VALUES (?, ?, 'running', ?, ?)`,
		templateID, toVersion, at, by)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertRolloutItem adds a server to a rollout.
func InsertRolloutItem(ctx context.Context, tx sqlx.ExtContext, it RolloutItem) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_rollout_items (rollout_id, position, server_id, from_version, params, params_secret, state, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))`, it.RolloutID, it.Position, it.ServerID, it.FromVersion, it.Params, it.ParamsSecret, it.State, it.Error)
	return err
}

// GetRollout returns one rollout.
func GetRollout(ctx context.Context, q sqlx.QueryerContext, id int64) (Rollout, error) {
	var r Rollout
	return r, notFound(sqlx.GetContext(ctx, q, &r, rolloutSelect+` WHERE id = ?`, id))
}

// RolloutItems returns a rollout's items in order.
func RolloutItems(ctx context.Context, q sqlx.QueryerContext, id int64) ([]RolloutItem, error) {
	var out []RolloutItem
	err := sqlx.SelectContext(ctx, q, &out, itemSelect+` WHERE rollout_id = ? ORDER BY position`, id)
	return out, err
}

// RolloutItemForJob is the item a deploy job belongs to.
func RolloutItemForJob(ctx context.Context, q sqlx.QueryerContext, rolloutID, jobID int64) (RolloutItem, bool, error) {
	var it RolloutItem
	err := sqlx.GetContext(ctx, q, &it, itemSelect+` WHERE rollout_id = ? AND job_id = ?`, rolloutID, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return RolloutItem{}, false, nil
	}
	return it, err == nil, err
}

// SetRolloutItem moves an item to a state; jobID 0 leaves the job as it is.
func SetRolloutItem(ctx context.Context, tx sqlx.ExtContext, rolloutID int64, position int, state string, jobID int64, errText string) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_rollout_items SET state = ?, job_id = CASE WHEN ? = 0 THEN job_id ELSE ? END, error = NULLIF(?, '')
		WHERE rollout_id = ? AND position = ?`, state, jobID, jobID, errText, rolloutID, position)
	return err
}

// SetRolloutState changes a rollout's state.
func SetRolloutState(ctx context.Context, tx sqlx.ExtContext, id int64, state string) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_rollouts SET state = ? WHERE id = ?`, state, id)
	return err
}

// FinishRollout stamps the end of a rollout once; it reports whether this call
// was the one (a rollout that already has an end stamp is left alone).
func FinishRollout(ctx context.Context, tx sqlx.ExtContext, id int64, at db.Time) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE servers_rollouts SET finished_at = ? WHERE id = ? AND finished_at IS NULL`, at, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListRollouts returns the newest rollouts first.
func ListRollouts(ctx context.Context, q sqlx.QueryerContext, limit int) ([]Rollout, error) {
	var out []Rollout
	err := sqlx.SelectContext(ctx, q, &out, rolloutSelect+` ORDER BY id DESC LIMIT ?`, limit)
	return out, err
}

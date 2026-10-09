package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// DiscoveryRetention is how long discovery runs and their screenshots are kept.
const DiscoveryRetention = 30 * 24 * time.Hour

// DiscoveryRun is a row of routing_discovery_runs. Suggestions and Visits
// are JSON the discovery package reads.
type DiscoveryRun struct {
	ID          int64         `db:"id"`
	Input       string        `db:"input"`
	URL         string        `db:"url"`
	Host        string        `db:"host"`
	Registrable string        `db:"registrable"`
	Via         string        `db:"via"`
	ServerID    sql.NullInt64 `db:"server_id"`
	ServerName  string        `db:"server_name"`
	Depth       int           `db:"depth"`
	State       string        `db:"state"`
	JobID       sql.NullInt64 `db:"job_id"`
	Title       string        `db:"title"`
	Suggestions string        `db:"suggestions"`
	Visits      string        `db:"visits"`
	Capped      bool          `db:"capped"`
	Error       string        `db:"error"`
	CreatedAt   db.Time       `db:"created_at"`
	FinishedAt  db.Time       `db:"finished_at"`
}

const runCols = `id, input, url, host, registrable, via, server_id, server_name, depth, state, job_id, title, suggestions, visits,
	capped, error, created_at, finished_at`

// DiscoveredHost is a row of routing_discovered_hosts. Failed is the direct
// visit's failure as "<requests> <reason>" ("3 net::ERR_CONNECTION_RESET"),
// ” for none.
type DiscoveredHost struct {
	RunID       int64  `db:"run_id"`
	Host        string `db:"host"`
	Registrable string `db:"registrable"`
	Class       string `db:"class"`
	Requests    int    `db:"requests"`
	Failed      string `db:"failed"`
	Seen        string `db:"seen"`
}

// InsertRun stores a queued run and returns its id.
func InsertRun(ctx context.Context, x sqlx.ExtContext, r DiscoveryRun) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO routing_discovery_runs (input, url, host, registrable, via, server_id, server_name, depth, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?)`,
		r.Input, r.URL, r.Host, r.Registrable, r.Via, r.ServerID, r.ServerName, r.Depth, r.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetRunJob records the run's job.
func SetRunJob(ctx context.Context, x sqlx.ExtContext, id, jobID int64) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_discovery_runs SET job_id = ? WHERE id = ?`, jobID, id)
	return err
}

// GetRun reads one run.
func GetRun(ctx context.Context, q sqlx.QueryerContext, id int64) (DiscoveryRun, error) {
	var r DiscoveryRun
	err := sqlx.GetContext(ctx, q, &r, `SELECT `+runCols+` FROM routing_discovery_runs WHERE id = ?`, id)
	return r, notFound(err)
}

// RunOfJob reads the run a job works on.
func RunOfJob(ctx context.Context, q sqlx.QueryerContext, jobID int64) (DiscoveryRun, error) {
	var r DiscoveryRun
	err := sqlx.GetContext(ctx, q, &r, `SELECT `+runCols+` FROM routing_discovery_runs WHERE job_id = ?`, jobID)
	return r, notFound(err)
}

// RecentRuns reads the newest runs first.
func RecentRuns(ctx context.Context, q sqlx.QueryerContext, limit int) ([]DiscoveryRun, error) {
	var out []DiscoveryRun
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+runCols+` FROM routing_discovery_runs ORDER BY id DESC LIMIT ?`, limit)
	return out, err
}

// RunLookedUp stores the catalog's suggestions and marks the run running.
func RunLookedUp(ctx context.Context, x sqlx.ExtContext, id int64, suggestions string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_discovery_runs SET state = 'running', suggestions = ? WHERE id = ?`, suggestions, id)
	return err
}

// SaveRunVisits replaces the run's visits and hosts with what the visits so far saw.
func SaveRunVisits(ctx context.Context, x sqlx.ExtContext, id int64, title, visits string, capped bool, hosts []DiscoveredHost) error {
	if _, err := x.ExecContext(ctx, `UPDATE routing_discovery_runs SET state = 'running', title = ?, visits = ?, capped = ? WHERE id = ?`,
		title, visits, capped, id); err != nil {
		return err
	}
	if _, err := x.ExecContext(ctx, `DELETE FROM routing_discovered_hosts WHERE run_id = ?`, id); err != nil {
		return err
	}
	for i := 0; i < len(hosts); i += 200 {
		if _, err := sqlx.NamedExecContext(ctx, x, `INSERT INTO routing_discovered_hosts (run_id, host, registrable, class, requests, failed, seen)
			VALUES (:run_id, :host, :registrable, :class, :requests, :failed, :seen)`, hosts[i:min(i+200, len(hosts))]); err != nil {
			return err
		}
	}
	return nil
}

// RunHosts reads a run's hosts by name.
func RunHosts(ctx context.Context, q sqlx.QueryerContext, id int64) ([]DiscoveredHost, error) {
	var out []DiscoveredHost
	err := sqlx.SelectContext(ctx, q, &out, `SELECT run_id, host, registrable, class, requests, failed, seen
		FROM routing_discovered_hosts WHERE run_id = ? ORDER BY host`, id)
	return out, err
}

// FinishRun ends a run: done, or failed with its error.
func FinishRun(ctx context.Context, x sqlx.ExtContext, id int64, state, errText string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_discovery_runs SET state = ?, error = ?, finished_at = ? WHERE id = ?`, state, errText, at, id)
	return err
}

// RunsBefore lists the ids of runs created before the time.
func RunsBefore(ctx context.Context, q sqlx.QueryerContext, before db.Time) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM routing_discovery_runs WHERE created_at < ? ORDER BY id`, before)
	return out, err
}

// RunExists reports whether a run of that id is stored.
func RunExists(ctx context.Context, q sqlx.QueryerContext, id int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM routing_discovery_runs WHERE id = ?`, id)
	return n > 0, err
}

// DeleteRun deletes a run with its hosts.
func DeleteRun(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_discovery_runs WHERE id = ?`, id)
	return err
}

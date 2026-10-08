package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Health is the health columns of an active server (the rest of the row is
// Server). Every statement that moves them is here.
type Health struct {
	ServerID       int64          `db:"id"`
	State          string         `db:"state"`
	Health         string         `db:"health"`
	Retiring       bool           `db:"retiring"`
	Since          db.Time        `db:"health_since"`
	Reason         string         `db:"health_reason"` // JSON {"key","args"}
	Detail         string         `db:"health_detail"` // JSON
	Candidate      sql.NullString `db:"candidate"`
	CandidateCount int            `db:"candidate_count"`
	CountedProxyAt db.Time        `db:"counted_proxy_at"`
	PausedUntil    db.Time        `db:"checks_paused_until"`
	RemindedAt     db.Time        `db:"reminded_at"`
	CertWarned     bool           `db:"cert_warned"`
	DiskWarned     bool           `db:"disk_warned"`
}

const healthSelect = `SELECT id, state, COALESCE(health, '') AS health, retire_job_id IS NOT NULL AS retiring, health_since, health_reason,
	health_detail, candidate, candidate_count, counted_proxy_at, checks_paused_until, reminded_at, cert_warned, disk_warned
	FROM servers_servers`

// PausedForever is checks_paused_until for "until resumed".
var PausedForever = db.At(mustTime("9999-12-31T00:00:00.000Z"))

// GetHealth returns a server's health columns.
func GetHealth(ctx context.Context, q sqlx.QueryerContext, id int64) (Health, error) {
	var h Health
	return h, notFound(sqlx.GetContext(ctx, q, &h, healthSelect+` WHERE id = ?`, id))
}

// ListHealth returns the health columns of every active server.
func ListHealth(ctx context.Context, q sqlx.QueryerContext) ([]Health, error) {
	var out []Health
	err := sqlx.SelectContext(ctx, q, &out, healthSelect+` WHERE state = 'active' ORDER BY id`)
	return out, err
}

// CheckableServerIDs are the servers a check round covers: active, not being
// retired, checks not paused.
func CheckableServerIDs(ctx context.Context, q sqlx.QueryerContext) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM servers_servers
		WHERE state = 'active' AND retire_job_id IS NULL AND checks_paused_until IS NULL ORDER BY id`)
	return out, err
}

// SetHealthState changes a server's health, since and reason, and starts the
// reminder clock over.
func SetHealthState(ctx context.Context, tx sqlx.ExtContext, id int64, to string, since db.Time, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET health = ?, health_since = ?, health_reason = ?, reminded_at = NULL
		WHERE id = ? AND state = 'active'`, to, since, reason, id)
	return err
}

// SetEvaluation stores the outcome of an evaluation that is not a state
// change: the candidate and its run, the proxy round it used and the detail.
// A zero countedProxyAt keeps the stored one.
func SetEvaluation(ctx context.Context, tx sqlx.ExtContext, id int64, candidate string, count int, countedProxyAt db.Time, detail string) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET candidate = NULLIF(?, ''), candidate_count = ?,
		counted_proxy_at = CASE WHEN ? IS NULL THEN counted_proxy_at ELSE ? END, health_detail = ? WHERE id = ?`,
		candidate, count, countedProxyAt, countedProxyAt, detail, id)
	return err
}

// SetDetail replaces the detail of the latest evaluation.
func SetDetail(ctx context.Context, tx sqlx.ExtContext, id int64, detail string) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET health_detail = ? WHERE id = ?`, detail, id)
	return err
}

// ResetCandidates clears every server's candidate run: home came back, and
// results from before the outage must not count toward a confirmation.
func ResetCandidates(ctx context.Context, tx sqlx.ExtContext) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET candidate = NULL, candidate_count = 0 WHERE state = 'active'`)
	return err
}

// SetPaused marks checks paused until a time.
func SetPaused(ctx context.Context, tx sqlx.ExtContext, id int64, until db.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET checks_paused_until = ?, candidate = NULL, candidate_count = 0 WHERE id = ?`, until, id)
	return err
}

// ClearPaused ends a pause: unknown until new results arrive.
func ClearPaused(ctx context.Context, tx sqlx.ExtContext, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET checks_paused_until = NULL, candidate = NULL, candidate_count = 0,
		counted_proxy_at = NULL WHERE id = ?`, id)
	return err
}

// SetReminded records the time of the last still-unhealthy reminder.
func SetReminded(ctx context.Context, tx sqlx.ExtContext, id int64, at db.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_servers SET reminded_at = ? WHERE id = ?`, at, id)
	return err
}

// SetWarned sets the once-per-crossing flags; a nil pointer leaves a flag.
func SetWarned(ctx context.Context, tx sqlx.ExtContext, id int64, cert, disk *bool) error {
	if cert != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE servers_servers SET cert_warned = ? WHERE id = ?`, *cert, id); err != nil {
			return err
		}
	}
	if disk != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE servers_servers SET disk_warned = ? WHERE id = ?`, *disk, id); err != nil {
			return err
		}
	}
	return nil
}

// --- check results

// CheckResult is one stored result.
type CheckResult struct {
	ID           int64   `db:"id"`
	ServerID     int64   `db:"server_id"`
	Kind         string  `db:"kind"` // self proxy external
	EndpointKey  string  `db:"endpoint_key"`
	Vantage      string  `db:"vantage"`
	At           db.Time `db:"at"`
	OK           bool    `db:"ok"`
	Class        string  `db:"class"`
	Inconclusive bool    `db:"inconclusive"`
	Detail       string  `db:"detail"` // JSON
}

// InsertCheckResult stores a result.
func InsertCheckResult(ctx context.Context, tx sqlx.ExtContext, r CheckResult) error {
	if r.Detail == "" {
		r.Detail = "{}"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_check_results (server_id, kind, endpoint_key, vantage, at, ok, class, inconclusive, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ServerID, r.Kind, r.EndpointKey, r.Vantage, r.At, r.OK, r.Class, r.Inconclusive, r.Detail)
	return err
}

const resultColumns = `id, server_id, kind, endpoint_key, vantage, at, ok, class, inconclusive, detail`

// LatestResults returns, for a kind, the newest result of every (endpoint,
// vantage) pair of the server, whatever its age or status; callers apply
// freshness and the inconclusive rule.
func LatestResults(ctx context.Context, q sqlx.QueryerContext, serverID int64, kind string) ([]CheckResult, error) {
	var out []CheckResult
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+resultColumns+` FROM servers_check_results r
		WHERE server_id = ? AND kind = ? AND at = (SELECT max(at) FROM servers_check_results r2
			WHERE r2.server_id = r.server_id AND r2.kind = r.kind AND r2.endpoint_key = r.endpoint_key AND r2.vantage = r.vantage)
		ORDER BY endpoint_key, vantage, id`, serverID, kind)
	return out, err
}

// ResultsSince returns a kind's results of the server from a time on, oldest
// first (sparklines and success rates).
func ResultsSince(ctx context.Context, q sqlx.QueryerContext, serverID int64, kind string, since db.Time) ([]CheckResult, error) {
	var out []CheckResult
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+resultColumns+` FROM servers_check_results
		WHERE server_id = ? AND kind = ? AND at >= ? ORDER BY at, id`, serverID, kind, since)
	return out, err
}

// LastExternal is the time of the server's newest external check that was
// really made (a skipped one does not count), if any.
func LastExternal(ctx context.Context, q sqlx.QueryerContext, serverID int64) (db.Time, bool, error) {
	var t db.Time
	err := sqlx.GetContext(ctx, q, &t, `SELECT at FROM servers_check_results
		WHERE server_id = ? AND kind = 'external' AND class <> 'skipped' ORDER BY at DESC LIMIT 1`, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Time{}, false, nil
	}
	return t, err == nil, err
}

// ExternalChecksSince counts the external checks made since a time, over all
// servers: one check is the batch of rows stored together.
func ExternalChecksSince(ctx context.Context, q sqlx.QueryerContext, since db.Time) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM (SELECT DISTINCT server_id, at FROM servers_check_results
		WHERE kind = 'external' AND class <> 'skipped' AND at >= ?)`, since)
	return n, err
}

// PruneResults deletes check results and reference results older than a time.
func PruneResults(ctx context.Context, tx sqlx.ExtContext, before db.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers_check_results WHERE at < ?`, before); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM servers_reference_results WHERE at < ?`, before)
	return err
}

// --- home

// Reference states.
const (
	HomeOnline             = "online"
	HomeForeignUnreachable = "foreign-unreachable"
	HomeOffline            = "offline"
)

// GetHome returns home's connectivity; a fresh install is online since never.
func GetHome(ctx context.Context, q sqlx.QueryerContext) (state string, since db.Time, err error) {
	var row struct {
		State string  `db:"state"`
		Since db.Time `db:"since"`
	}
	err = sqlx.GetContext(ctx, q, &row, `SELECT state, since FROM servers_home WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return HomeOnline, db.Time{}, nil
	}
	return row.State, row.Since, err
}

// SetHome records home's connectivity from a time on.
func SetHome(ctx context.Context, tx sqlx.ExtContext, state string, since db.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_home (id, state, since) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET state = excluded.state, since = excluded.since`, state, since)
	return err
}

// InsertReference stores one reference check.
func InsertReference(ctx context.Context, tx sqlx.ExtContext, at db.Time, result, detail string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_reference_results (at, result, detail) VALUES (?, ?, ?)`, at, result, detail)
	return err
}

// Reference is a stored reference check.
type Reference struct {
	At     db.Time `db:"at"`
	Result string  `db:"result"`
	Detail string  `db:"detail"`
}

// RecentReferences returns the newest reference checks, newest first.
func RecentReferences(ctx context.Context, q sqlx.QueryerContext, limit int) ([]Reference, error) {
	var out []Reference
	err := sqlx.SelectContext(ctx, q, &out, `SELECT at, result, detail FROM servers_reference_results ORDER BY id DESC LIMIT ?`, limit)
	return out, err
}

// --- check-host nodes

// CheckhostNode is a known external node.
type CheckhostNode struct {
	Name        string  `db:"name"`
	Country     string  `db:"country"`
	City        string  `db:"city"`
	RefreshedAt db.Time `db:"refreshed_at"`
}

// CheckhostNodes returns the known nodes, by name.
func CheckhostNodes(ctx context.Context, q sqlx.QueryerContext) ([]CheckhostNode, error) {
	var out []CheckhostNode
	err := sqlx.SelectContext(ctx, q, &out, `SELECT name, country, city, refreshed_at FROM servers_checkhost_nodes ORDER BY name`)
	return out, err
}

// ReplaceCheckhostNodes makes nodes the known list.
func ReplaceCheckhostNodes(ctx context.Context, tx sqlx.ExtContext, nodes []CheckhostNode, at db.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers_checkhost_nodes`); err != nil {
		return err
	}
	for _, n := range nodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers_checkhost_nodes (name, country, city, refreshed_at) VALUES (?, ?, ?, ?)`,
			n.Name, n.Country, n.City, at); err != nil {
			return err
		}
	}
	return nil
}

package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Previous IP states (docs/processes/servers/ip-change.md).
const (
	PreviousUnknown = "unknown"
	PreviousWorks   = "works"
	PreviousFails   = "fails"
)

// PreviousIP is an IP a server used before an IP change and still answers on.
type PreviousIP struct {
	ID             int64          `db:"id"`
	ServerID       int64          `db:"server_id"`
	IP             string         `db:"ip"`
	Since          db.Time        `db:"since"`
	State          string         `db:"state"`
	StateSince     db.Time        `db:"state_since"`
	Candidate      sql.NullString `db:"candidate"`
	CandidateCount int            `db:"candidate_count"`
}

const previousSelect = `SELECT id, server_id, ip, since, state, state_since, candidate, candidate_count FROM servers_previous_ips`

// PreviousIPs returns a server's previous IPs, the latest to leave first.
func PreviousIPs(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]PreviousIP, error) {
	var out []PreviousIP
	err := sqlx.SelectContext(ctx, q, &out, previousSelect+` WHERE server_id = ? ORDER BY since DESC, id DESC`, serverID)
	return out, err
}

// GetPreviousIP returns one previous IP of a server.
func GetPreviousIP(ctx context.Context, q sqlx.QueryerContext, serverID, id int64) (PreviousIP, error) {
	var p PreviousIP
	return p, notFound(sqlx.GetContext(ctx, q, &p, previousSelect+` WHERE server_id = ? AND id = ?`, serverID, id))
}

// ServersWithPreviousIPs are the active, unpaused, not retiring servers that
// keep a previous IP: what a previous IP round tests.
func ServersWithPreviousIPs(ctx context.Context, q sqlx.QueryerContext) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM servers_servers s
		WHERE state = 'active' AND retire_job_id IS NULL AND checks_paused_until IS NULL
		AND EXISTS (SELECT 1 FROM servers_previous_ips p WHERE p.server_id = s.id) ORDER BY id`)
	return out, err
}

// IPHolder is the server that has an IP, as its IP or a previous one.
type IPHolder struct {
	ServerID int64  `db:"id"`
	Name     string `db:"name"`
	Previous bool   `db:"previous"`
}

// HolderOfIP returns the non-retired server that has ip, as its IP or as a
// previous IP. Two servers never share one: the checks of one would reach
// the other.
func HolderOfIP(ctx context.Context, q sqlx.QueryerContext, ip string) (IPHolder, bool, error) {
	var h IPHolder
	err := sqlx.GetContext(ctx, q, &h, `SELECT id, name, 0 AS previous FROM servers_servers WHERE ip = ? AND state <> 'retired'
		UNION ALL
		SELECT s.id, s.name, 1 FROM servers_previous_ips p JOIN servers_servers s ON s.id = p.server_id
			WHERE p.ip = ? AND s.state <> 'retired'
		UNION ALL
		SELECT s.id, s.name, 0 FROM servers_addresses a JOIN servers_servers s ON s.id = a.server_id
			WHERE a.ip = ? AND s.state <> 'retired'
		LIMIT 1`, ip, ip, ip)
	if errors.Is(err, sql.ErrNoRows) {
		return IPHolder{}, false, nil
	}
	return h, err == nil, err
}

// HolderOfHostname returns the name of the non-retired server that has host
// as its management or proxy hostname. Two servers never share one: both
// would want the same DNS record.
func HolderOfHostname(ctx context.Context, q sqlx.QueryerContext, host string) (string, bool, error) {
	var name string
	err := sqlx.GetContext(ctx, q, &name, `SELECT name FROM servers_servers
		WHERE (management_hostname = ? OR proxy_hostname = ?) AND state <> 'retired'
			OR EXISTS (SELECT 1 FROM servers_addresses a WHERE a.server_id = servers_servers.id
				AND a.hostname = ? AND servers_servers.state <> 'retired')
		LIMIT 1`, host, host, host)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// SetServerIP moves a server to ip and starts its checks over from at: the
// health state is unknown, no candidate counts.
func SetServerIP(ctx context.Context, tx sqlx.ExtContext, id int64, ip string, at db.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE servers_servers SET ip = ?, ip_changed_at = ?, candidate = NULL, candidate_count = 0,
		counted_proxy_at = NULL WHERE id = ? AND state = 'active'`, ip, at, id)
	if unique(err, "servers_servers.ip") {
		return ErrIPInUse
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddPreviousIP keeps ip as a previous IP of the server from at, its state
// unknown until it is tested.
func AddPreviousIP(ctx context.Context, tx sqlx.ExtContext, serverID int64, ip string, at db.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_previous_ips (server_id, ip, since, state, state_since) VALUES (?, ?, ?, 'unknown', ?)`,
		serverID, ip, at, at)
	if unique(err, "servers_previous_ips.ip") {
		return ErrIPInUse
	}
	return err
}

// DeletePreviousIP forgets a previous IP and its results; it reports whether
// there was one.
func DeletePreviousIP(ctx context.Context, tx sqlx.ExtContext, serverID int64, ip string) (bool, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM servers_previous_ips WHERE server_id = ? AND ip = ?`, serverID, ip)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetPreviousState moves a previous IP to a state from at; the candidate run
// starts over.
func SetPreviousState(ctx context.Context, tx sqlx.ExtContext, id int64, state string, at db.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_previous_ips SET state = ?, state_since = ?, candidate = NULL, candidate_count = 0
		WHERE id = ?`, state, at, id)
	return err
}

// SetPreviousCandidate records a run of results that disagree with the state.
func SetPreviousCandidate(ctx context.Context, tx sqlx.ExtContext, id int64, candidate string, count int) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_previous_ips SET candidate = NULLIF(?, ''), candidate_count = ? WHERE id = ?`,
		candidate, count, id)
	return err
}

// PreviousResult is one proxy test of a previous IP.
type PreviousResult struct {
	ID           int64   `db:"id"`
	PreviousID   int64   `db:"previous_ip_id"`
	At           db.Time `db:"at"`
	OK           bool    `db:"ok"`
	Class        string  `db:"class"`
	Inconclusive bool    `db:"inconclusive"`
	Detail       string  `db:"detail"` // JSON
}

const previousResultColumns = `id, previous_ip_id, at, ok, class, inconclusive, detail`

// InsertPreviousResult stores a test of a previous IP.
func InsertPreviousResult(ctx context.Context, tx sqlx.ExtContext, r PreviousResult) error {
	if r.Detail == "" {
		r.Detail = "{}"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_previous_ip_results (previous_ip_id, at, ok, class, inconclusive, detail)
		VALUES (?, ?, ?, ?, ?, ?)`, r.PreviousID, r.At, r.OK, r.Class, r.Inconclusive, r.Detail)
	return err
}

// PreviousResultsSince returns a previous IP's results from a time on, oldest
// first.
func PreviousResultsSince(ctx context.Context, q sqlx.QueryerContext, id int64, since db.Time) ([]PreviousResult, error) {
	var out []PreviousResult
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+previousResultColumns+` FROM servers_previous_ip_results
		WHERE previous_ip_id = ? AND at >= ? ORDER BY at, id`, id, since)
	return out, err
}

// LatestPreviousResult is a previous IP's newest result, if any.
func LatestPreviousResult(ctx context.Context, q sqlx.QueryerContext, id int64) (PreviousResult, bool, error) {
	var r PreviousResult
	err := sqlx.GetContext(ctx, q, &r, `SELECT `+previousResultColumns+` FROM servers_previous_ip_results
		WHERE previous_ip_id = ? ORDER BY at DESC, id DESC LIMIT 1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return PreviousResult{}, false, nil
	}
	return r, err == nil, err
}

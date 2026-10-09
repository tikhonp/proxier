package store

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Router is a row of routing_routers with its list's name.
type Router struct {
	ID                int64   `db:"id"`
	Name              string  `db:"name"`
	ListID            int64   `db:"list_id"`
	List              string  `db:"list"`
	State             string  `db:"state"`
	Host              string  `db:"host"`
	Port              int     `db:"port"`
	User              string  `db:"ssh_user"`
	JumpHost          string  `db:"jump_host"`
	JumpPort          int     `db:"jump_port"`
	JumpUser          string  `db:"jump_user"`
	Tailnet           bool    `db:"tailnet"`
	AddressList       string  `db:"address_list"`
	Forwarder         string  `db:"doh_forwarder"`
	Version           string  `db:"version"`
	Board             string  `db:"board"`
	ConnectedAt       db.Time `db:"connected_at"`
	LastSeenAt        db.Time `db:"last_seen_at"`
	LastSyncAt        db.Time `db:"last_sync_at"`
	LastSyncResult    string  `db:"last_sync_result"`
	LastSyncError     string  `db:"last_sync_error"`
	Failures          int     `db:"failures"`
	FailureNotified   bool    `db:"failure_notified"`
	Untagged          int     `db:"untagged"`
	InfraPins         int     `db:"infra_pins"`
	ReadAt            db.Time `db:"read_at"`
	Drift             string  `db:"drift"`
	DriftCheckedAt    db.Time `db:"drift_checked_at"`
	UnmanagedNotified string  `db:"unmanaged_notified"`
	AwaitingUntil     db.Time `db:"awaiting_until"`
	CreatedBy         string  `db:"created_by"`
	CreatedAt         db.Time `db:"created_at"`
}

const routerSelect = `SELECT r.id, r.name, r.list_id, l.name AS list, r.state, r.host, r.port, r.ssh_user, r.jump_host, r.jump_port,
	r.jump_user, r.tailnet, r.address_list, r.doh_forwarder, r.version, r.board, r.connected_at, r.last_seen_at, r.last_sync_at,
	r.last_sync_result, r.last_sync_error, r.failures, r.failure_notified, r.untagged, r.infra_pins, r.read_at,
	r.drift, r.drift_checked_at, r.unmanaged_notified, r.awaiting_until, r.created_by, r.created_at
	FROM routing_routers r JOIN routing_lists l ON l.id = r.list_id`

// Routers reads every router by name.
func Routers(ctx context.Context, q sqlx.QueryerContext) ([]Router, error) {
	var out []Router
	err := sqlx.SelectContext(ctx, q, &out, routerSelect+` ORDER BY r.name`)
	return out, err
}

// RoutersOfList reads the routers following a list, by name.
func RoutersOfList(ctx context.Context, q sqlx.QueryerContext, listID int64) ([]Router, error) {
	var out []Router
	err := sqlx.SelectContext(ctx, q, &out, routerSelect+` WHERE r.list_id = ? ORDER BY r.name`, listID)
	return out, err
}

// GetRouter reads one router.
func GetRouter(ctx context.Context, q sqlx.QueryerContext, id int64) (Router, error) {
	var r Router
	err := sqlx.GetContext(ctx, q, &r, routerSelect+` WHERE r.id = ?`, id)
	return r, notFound(err)
}

// RouterNameTaken reports whether another router has the name.
func RouterNameTaken(ctx context.Context, q sqlx.QueryerContext, name string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM routing_routers WHERE name = ? AND id <> ?`, name, except)
	return n > 0, err
}

// InsertRouter adds a router; the state, connection, names, version and
// connection times come from r.
func InsertRouter(ctx context.Context, x sqlx.ExtContext, r Router) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO routing_routers (name, list_id, state, host, port, ssh_user, jump_host, jump_port,
		jump_user, tailnet, address_list, doh_forwarder, version, board, connected_at, last_seen_at, awaiting_until, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.ListID, r.State, r.Host, r.Port, r.User, r.JumpHost, r.JumpPort, r.JumpUser, r.Tailnet,
		r.AddressList, r.Forwarder, r.Version, r.Board, r.ConnectedAt, r.LastSeenAt, r.AwaitingUntil, r.CreatedBy, r.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetRouterConnection writes a router's name, connection and names.
func SetRouterConnection(ctx context.Context, x sqlx.ExtContext, r Router) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET name = ?, host = ?, port = ?, ssh_user = ?, jump_host = ?, jump_port = ?,
		jump_user = ?, tailnet = ?, address_list = ?, doh_forwarder = ? WHERE id = ?`,
		r.Name, r.Host, r.Port, r.User, r.JumpHost, r.JumpPort, r.JumpUser, r.Tailnet, r.AddressList, r.Forwarder, r.ID)
	return err
}

// RouterSeen writes what a connection read: version, board and the time.
func RouterSeen(ctx context.Context, x sqlx.ExtContext, id int64, version, board string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET version = ?, board = ?, last_seen_at = ? WHERE id = ?`, version, board, at, id)
	return err
}

// RouterConnected sets the first successful connection's time.
func RouterConnected(ctx context.Context, x sqlx.ExtContext, id int64, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET connected_at = ? WHERE id = ? AND connected_at IS NULL`, at, id)
	return err
}

// RouterRead writes what the last read counted.
func RouterRead(ctx context.Context, x sqlx.ExtContext, id int64, untagged, pins int, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET untagged = ?, infra_pins = ?, read_at = ? WHERE id = ?`, untagged, pins, at, id)
	return err
}

// RouterSynced ends a router's failures after a successful sync; the
// router matches what Proxier applied again, so its drift is cleared too.
func RouterSynced(ctx context.Context, x sqlx.ExtContext, id int64, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET failures = 0, failure_notified = 0, last_sync_at = ?,
		last_sync_result = 'synced', last_sync_error = '', drift = '' WHERE id = ?`, at, id)
	return err
}

// RouterFailed counts a failed sync attempt; notified marks a failure that notified.
func RouterFailed(ctx context.Context, x sqlx.ExtContext, id int64, text string, notified bool, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET failures = failures + 1, last_sync_result = 'failed',
		last_sync_error = ?, last_sync_at = ?, failure_notified = max(failure_notified, ?) WHERE id = ?`, text, at, notified, id)
	return err
}

// MarkedRouters are the active routers that have connected at least once and
// follow one of the lists, a list holding one of the services, or are one of
// the routers: the routers a change syncs.
func MarkedRouters(ctx context.Context, q sqlx.QueryerContext, lists, services, routers []int64) ([]int64, error) {
	var conds []string
	var args []any
	in := func(col string, ids []int64) {
		if len(ids) == 0 {
			return
		}
		conds = append(conds, col+` IN (?`+strings.Repeat(`, ?`, len(ids)-1)+`)`)
		for _, id := range ids {
			args = append(args, id)
		}
	}
	in("list_id", lists)
	if len(services) > 0 {
		conds = append(conds, `list_id IN (SELECT list_id FROM routing_list_services WHERE service_id IN (?`+strings.Repeat(`, ?`, len(services)-1)+`))`)
		for _, id := range services {
			args = append(args, id)
		}
	}
	in("id", routers)
	if len(conds) == 0 {
		return nil, nil
	}
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM routing_routers WHERE state = 'active' AND connected_at IS NOT NULL
		AND (`+strings.Join(conds, " OR ")+`) ORDER BY id`, args...)
	return out, err
}

// RouterTag is what Proxier last installed under a tag (routing_router_tags).
type RouterTag struct {
	Tag       string  `db:"tag"`
	Hash      string  `db:"hash"`
	Suffix    int     `db:"suffix"`
	Exact     int     `db:"exact"`
	AppliedAt db.Time `db:"applied_at"`
}

// RouterTags reads a router's applied tags, by tag.
func RouterTags(ctx context.Context, q sqlx.QueryerContext, id int64) ([]RouterTag, error) {
	var out []RouterTag
	err := sqlx.SelectContext(ctx, q, &out, `SELECT tag, hash, suffix, exact, applied_at FROM routing_router_tags WHERE router_id = ? ORDER BY tag`, id)
	return out, err
}

// SetRouterTag records a tag as applied.
func SetRouterTag(ctx context.Context, x sqlx.ExtContext, id int64, t RouterTag) error {
	_, err := x.ExecContext(ctx, `INSERT INTO routing_router_tags (router_id, tag, hash, suffix, exact, applied_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (router_id, tag) DO UPDATE SET hash = excluded.hash, suffix = excluded.suffix, exact = excluded.exact, applied_at = excluded.applied_at`,
		id, t.Tag, t.Hash, t.Suffix, t.Exact, t.AppliedAt)
	return err
}

// DeleteRouterTag forgets a tag's applied state.
func DeleteRouterTag(ctx context.Context, x sqlx.ExtContext, id int64, tag string) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_router_tags WHERE router_id = ? AND tag = ?`, id, tag)
	return err
}

// SetRouterState moves a router to active, paused or removing; it is no
// longer awaiting setup.
func SetRouterState(ctx context.Context, x sqlx.ExtContext, id int64, state string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET state = ?, awaiting_until = NULL WHERE id = ?`, state, id)
	return err
}

// RouterActivated makes an awaiting router active after its first contact.
func RouterActivated(ctx context.Context, x sqlx.ExtContext, id int64, version, board string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET state = 'active', awaiting_until = NULL, version = ?, board = ?,
		last_seen_at = ? WHERE id = ?`, version, board, at, id)
	return err
}

// SetRouterDrift stores the tags that drifted at a check ("" for none).
func SetRouterDrift(ctx context.Context, x sqlx.ExtContext, id int64, drift string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET drift = ?, drift_checked_at = ? WHERE id = ?`, drift, at, id)
	return err
}

// SetUnmanagedNotified stores the unmanaged tags already notified.
func SetUnmanagedNotified(ctx context.Context, x sqlx.ExtContext, id int64, tags string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET unmanaged_notified = ? WHERE id = ?`, tags, id)
	return err
}

// DeleteRouter deletes a router with its applied and unmanaged tags, syncs
// and tests.
func DeleteRouter(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_routers WHERE id = ?`, id)
	return err
}

// RoutersIn reads the ids of the routers in a state, by id.
func RoutersIn(ctx context.Context, q sqlx.QueryerContext, state string) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM routing_routers WHERE state = ? ORDER BY id`, state)
	return out, err
}

// DriftCheckable reads the active routers that have connected: the ones the
// drift round checks.
func DriftCheckable(ctx context.Context, q sqlx.QueryerContext) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM routing_routers WHERE state = 'active' AND connected_at IS NOT NULL ORDER BY id`)
	return out, err
}

// Probeable reads the routers awaiting setup whose probing hasn't ended.
func Probeable(ctx context.Context, q sqlx.QueryerContext, now db.Time) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM routing_routers WHERE state = 'awaiting' AND awaiting_until > ? ORDER BY id`, now)
	return out, err
}

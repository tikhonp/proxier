package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// MaxUnmanagedNames is how many names of an unmanaged tag are kept.
const MaxUnmanagedNames = 5000

// Unmanaged is a row of routing_router_unmanaged: a tag on a router that
// Proxier never installed.
type Unmanaged struct {
	RouterID  int64   `db:"router_id"`
	Tag       string  `db:"tag"`
	Entries   int     `db:"entries"`
	Names     string  `db:"names"` // JSON [{"name":…,"exact":…}]
	FirstSeen db.Time `db:"first_seen"`
	LastSeen  db.Time `db:"last_seen"`
	IgnoredAt db.Time `db:"ignored_at"`
}

const unmanagedCols = `router_id, tag, entries, names, first_seen, last_seen, ignored_at`

// UnmanagedOf reads a router's unmanaged tags, by tag.
func UnmanagedOf(ctx context.Context, q sqlx.QueryerContext, routerID int64) ([]Unmanaged, error) {
	var out []Unmanaged
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+unmanagedCols+` FROM routing_router_unmanaged WHERE router_id = ? ORDER BY tag`, routerID)
	return out, err
}

// GetUnmanaged reads one unmanaged tag.
func GetUnmanaged(ctx context.Context, q sqlx.QueryerContext, routerID int64, tag string) (Unmanaged, error) {
	var u Unmanaged
	err := sqlx.GetContext(ctx, q, &u, `SELECT `+unmanagedCols+` FROM routing_router_unmanaged WHERE router_id = ? AND tag = ?`, routerID, tag)
	return u, notFound(err)
}

// SeeUnmanaged writes what a read found under an unmanaged tag; first_seen
// and ignored_at stay as they were.
func SeeUnmanaged(ctx context.Context, x sqlx.ExtContext, u Unmanaged) error {
	_, err := x.ExecContext(ctx, `INSERT INTO routing_router_unmanaged (router_id, tag, entries, names, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (router_id, tag) DO UPDATE SET entries = excluded.entries, names = excluded.names, last_seen = excluded.last_seen`,
		u.RouterID, u.Tag, u.Entries, u.Names, u.FirstSeen, u.LastSeen)
	return err
}

// DeleteUnmanaged deletes an unmanaged tag's row.
func DeleteUnmanaged(ctx context.Context, x sqlx.ExtContext, routerID int64, tag string) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_router_unmanaged WHERE router_id = ? AND tag = ?`, routerID, tag)
	return err
}

// SetUnmanagedIgnored ignores a tag (at set) or stops ignoring it (zero at).
func SetUnmanagedIgnored(ctx context.Context, x sqlx.ExtContext, routerID int64, tag string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_router_unmanaged SET ignored_at = ? WHERE router_id = ? AND tag = ?`, at, routerID, tag)
	return err
}

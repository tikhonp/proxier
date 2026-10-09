package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// List is a row of routing_lists.
type List struct {
	ID          int64   `db:"id"`
	Name        string  `db:"name"`
	Description string  `db:"description"`
	IsDefault   bool    `db:"is_default"`
	CreatedAt   db.Time `db:"created_at"`
}

const listCols = `id, name, description, is_default, created_at`

// Lists reads every routing list: the default first, then by name.
func Lists(ctx context.Context, q sqlx.QueryerContext) ([]List, error) {
	var out []List
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+listCols+` FROM routing_lists ORDER BY is_default DESC, name`)
	return out, err
}

// GetList reads one by id.
func GetList(ctx context.Context, q sqlx.QueryerContext, id int64) (List, error) {
	var l List
	err := sqlx.GetContext(ctx, q, &l, `SELECT `+listCols+` FROM routing_lists WHERE id = ?`, id)
	return l, notFound(err)
}

// DefaultList reads the default list.
func DefaultList(ctx context.Context, q sqlx.QueryerContext) (List, error) {
	var l List
	err := sqlx.GetContext(ctx, q, &l, `SELECT `+listCols+` FROM routing_lists WHERE is_default = 1`)
	return l, notFound(err)
}

// ListNameTaken reports whether another list (not except) has the name.
func ListNameTaken(ctx context.Context, q sqlx.QueryerContext, name string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM routing_lists WHERE name = ? AND id <> ?`, name, except)
	return n > 0, err
}

// InsertList adds a list and returns its id.
func InsertList(ctx context.Context, x sqlx.ExtContext, l List) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO routing_lists (name, description, is_default, created_at) VALUES (?, ?, 0, ?)`,
		l.Name, l.Description, l.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetListFields writes a list's name and description.
func SetListFields(ctx context.Context, x sqlx.ExtContext, id int64, name, description string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_lists SET name = ?, description = ? WHERE id = ?`, name, description, id)
	return err
}

// MakeDefault moves the default mark to the list: the old mark goes first,
// so the one-default index holds in between.
func MakeDefault(ctx context.Context, x sqlx.ExtContext, id int64) error {
	if _, err := x.ExecContext(ctx, `UPDATE routing_lists SET is_default = 0 WHERE is_default = 1 AND id <> ?`, id); err != nil {
		return err
	}
	_, err := x.ExecContext(ctx, `UPDATE routing_lists SET is_default = 1 WHERE id = ?`, id)
	return err
}

// DeleteList deletes a list; its memberships go with it (ON DELETE CASCADE).
func DeleteList(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_lists WHERE id = ?`, id)
	return err
}

// ListsOf names the routing lists that hold a service, by name.
func ListsOf(ctx context.Context, q sqlx.QueryerContext, serviceID int64) ([]string, error) {
	var out []string
	err := sqlx.SelectContext(ctx, q, &out, `SELECT l.name FROM routing_list_services m JOIN routing_lists l ON l.id = m.list_id
		WHERE m.service_id = ? ORDER BY l.name`, serviceID)
	return out, err
}

// ListName reads a routing list's name.
func ListName(ctx context.Context, q sqlx.QueryerContext, id int64) (string, error) {
	var n string
	err := sqlx.GetContext(ctx, q, &n, `SELECT name FROM routing_lists WHERE id = ?`, id)
	return n, notFound(err)
}

// ListTarget is a router or a Shadowrocket config that follows a list.
type ListTarget struct {
	Kind      string  `db:"kind"` // router, shadowrocket
	ID        int64   `db:"id"`
	Name      string  `db:"name"`
	State     string  `db:"state"`
	LastFetch db.Time `db:"last_fetch"` // Shadowrocket: the last fetch; zero for none and for routers
}

// ListTargets reads the routers and Shadowrocket configs following a list:
// routers first, each by name.
func ListTargets(ctx context.Context, q sqlx.QueryerContext, listID int64) ([]ListTarget, error) {
	var out []ListTarget
	err := sqlx.SelectContext(ctx, q, &out, `
		SELECT 'router' AS kind, id, name, state, NULL AS last_fetch FROM routing_routers WHERE list_id = ?
		UNION ALL
		SELECT 'shadowrocket', id, name, CASE enabled WHEN 1 THEN 'enabled' ELSE 'disabled' END, last_fetch_at FROM routing_shadowrocket WHERE list_id = ?
		ORDER BY 1, 3`, listID, listID)
	return out, err
}

// MoveRouter makes a router follow another list.
func MoveRouter(ctx context.Context, x sqlx.ExtContext, id, listID int64) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_routers SET list_id = ? WHERE id = ?`, listID, id)
	return err
}

// MoveShadowrocket makes a Shadowrocket config follow another list.
func MoveShadowrocket(ctx context.Context, x sqlx.ExtContext, id, listID int64) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_shadowrocket SET list_id = ? WHERE id = ?`, listID, id)
	return err
}

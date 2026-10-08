package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

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

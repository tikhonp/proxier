package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Member is a service of a list at its position.
type Member struct {
	ServiceID int64   `db:"service_id"`
	Position  int     `db:"position"`
	AddedAt   db.Time `db:"added_at"`
}

// Members reads a list's services in order.
func Members(ctx context.Context, q sqlx.QueryerContext, listID int64) ([]Member, error) {
	var out []Member
	err := sqlx.SelectContext(ctx, q, &out, `SELECT service_id, position, added_at FROM routing_list_services
		WHERE list_id = ? ORDER BY position`, listID)
	return out, err
}

// AddMember appends a service at the position given.
func AddMember(ctx context.Context, x sqlx.ExtContext, listID, serviceID int64, position int, at db.Time) error {
	_, err := x.ExecContext(ctx, `INSERT INTO routing_list_services (list_id, service_id, position, added_at) VALUES (?, ?, ?, ?)`,
		listID, serviceID, position, at)
	return err
}

// RemoveMember takes a service out of a list; the caller re-densifies.
func RemoveMember(ctx context.Context, x sqlx.ExtContext, listID, serviceID int64) (bool, error) {
	res, err := x.ExecContext(ctx, `DELETE FROM routing_list_services WHERE list_id = ? AND service_id = ?`, listID, serviceID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetPositions writes the order: serviceIDs[i] gets position i+1.
func SetPositions(ctx context.Context, x sqlx.ExtContext, listID int64, serviceIDs []int64) error {
	for i, id := range serviceIDs {
		if _, err := x.ExecContext(ctx, `UPDATE routing_list_services SET position = ? WHERE list_id = ? AND service_id = ?`,
			i+1, listID, id); err != nil {
			return err
		}
	}
	return nil
}

// Membership is a list holding a service, at a position.
type Membership struct {
	ListID    int64  `db:"list_id"`
	Name      string `db:"name"`
	IsDefault bool   `db:"is_default"`
	Position  int    `db:"position"`
}

// MembershipsOf reads the lists that hold a service, by name.
func MembershipsOf(ctx context.Context, q sqlx.QueryerContext, serviceID int64) ([]Membership, error) {
	var out []Membership
	err := sqlx.SelectContext(ctx, q, &out, `SELECT m.list_id, l.name, l.is_default, m.position FROM routing_list_services m
		JOIN routing_lists l ON l.id = m.list_id WHERE m.service_id = ? ORDER BY l.name`, serviceID)
	return out, err
}

// ServicesInList reads the ids of a list's services (Filter.List).
func ServicesInList(ctx context.Context, q sqlx.QueryerContext, listID int64) (map[int64]bool, error) {
	var ids []int64
	if err := sqlx.SelectContext(ctx, q, &ids, `SELECT service_id FROM routing_list_services WHERE list_id = ?`, listID); err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

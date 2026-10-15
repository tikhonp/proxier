package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

// ownedTables are the tables whose rows have an owner, with the kind each
// counts as in routing.handed_over and the type of its events' subjects
// (imports have no events of their own). A shared service's owner is 0, so a
// member never has one to hand over.
var ownedTables = []struct{ Table, Kind, Subject string }{
	{"routing_lists", "lists", "routing_list"},
	{"routing_routers", "routers", "router"},
	{"routing_shadowrocket", "shadowrocket", "shadowrocket"},
	{"routing_services", "services", "service"},
	{"routing_imports", "imports", ""},
	{"routing_discovery_runs", "discovery", "discovery"},
}

// SubjectType is the subject type of a kind's events, "" for none.
func SubjectType(kind string) string {
	for _, t := range ownedTables {
		if t.Kind == kind {
			return t.Subject
		}
	}
	return ""
}

// OwnerOf is the owner of the thing an event's subject names. ok is false
// for a subject type that has no owner, a shared service, or a thing that is
// gone.
func OwnerOf(ctx context.Context, q sqlx.QueryerContext, subjectType string, id int64) (owner int64, ok bool, err error) {
	for _, t := range ownedTables {
		if t.Subject == "" || t.Subject != subjectType {
			continue
		}
		err = sqlx.GetContext(ctx, q, &owner, `SELECT owner_id FROM `+t.Table+` WHERE id = ?`, id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return owner, err == nil && owner != 0, err
	}
	return 0, false, nil
}

// HandOver gives every list, router, Shadowrocket config, custom service,
// import and discovery run of from to to: the count of each kind moved.
func HandOver(ctx context.Context, x sqlx.ExtContext, from, to int64) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range ownedTables {
		res, err := x.ExecContext(ctx, `UPDATE `+t.Table+` SET owner_id = ? WHERE owner_id = ?`, to, from)
		if err != nil {
			return nil, err
		}
		if out[t.Kind], err = res.RowsAffected(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// HandOverKinds are the kinds HandOver counts, in its order.
func HandOverKinds() []string {
	out := make([]string, len(ownedTables))
	for i, t := range ownedTables {
		out[i] = t.Kind
	}
	return out
}

// Owned are the ids of everything owner has, by the kind HandOver counts.
func Owned(ctx context.Context, q sqlx.QueryerContext, owner int64) (map[string][]int64, error) {
	out := map[string][]int64{}
	for _, t := range ownedTables {
		var ids []int64
		if err := sqlx.SelectContext(ctx, q, &ids, `SELECT id FROM `+t.Table+` WHERE owner_id = ? ORDER BY id`, owner); err != nil {
			return nil, err
		}
		out[t.Kind] = ids
	}
	return out, nil
}

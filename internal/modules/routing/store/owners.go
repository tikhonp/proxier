package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// ownedTables are the tables whose rows have an owner, with the kind each
// counts as in routing.handed_over. A shared service's owner is 0, so a
// member never has one to hand over.
var ownedTables = []struct{ Table, Kind string }{
	{"routing_lists", "lists"},
	{"routing_routers", "routers"},
	{"routing_shadowrocket", "shadowrocket"},
	{"routing_services", "services"},
	{"routing_imports", "imports"},
	{"routing_discovery_runs", "discovery"},
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

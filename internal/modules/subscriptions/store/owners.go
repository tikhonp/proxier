package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

// HandOver gives every subscription and link of from to to.
func HandOver(ctx context.Context, x sqlx.ExtContext, from, to int64) (subs, links int64, err error) {
	res, err := x.ExecContext(ctx, `UPDATE subs_subscriptions SET owner_id = ? WHERE owner_id = ?`, to, from)
	if err != nil {
		return 0, 0, err
	}
	if subs, err = res.RowsAffected(); err != nil {
		return 0, 0, err
	}
	res, err = x.ExecContext(ctx, `UPDATE subs_links SET owner_id = ? WHERE owner_id = ?`, to, from)
	if err != nil {
		return 0, 0, err
	}
	links, err = res.RowsAffected()
	return subs, links, err
}

// Owned are the ids of the subscriptions and links of owner, deleted links
// included.
func Owned(ctx context.Context, q sqlx.QueryerContext, owner int64) (subs, links []int64, err error) {
	if err = sqlx.SelectContext(ctx, q, &subs, `SELECT id FROM subs_subscriptions WHERE owner_id = ? ORDER BY id`, owner); err != nil {
		return nil, nil, err
	}
	err = sqlx.SelectContext(ctx, q, &links, `SELECT id FROM subs_links WHERE owner_id = ? ORDER BY id`, owner)
	return subs, links, err
}

// ownerTables are the tables of the event subject types whose things have an
// owner.
var ownerTables = map[string]string{"subscription": "subs_subscriptions", "link": "subs_links"}

// OwnerOf is the owner of the subscription or link an event's subject names;
// ok is false for another subject type or a thing that is gone.
func OwnerOf(ctx context.Context, q sqlx.QueryerContext, subjectType string, id int64) (owner int64, ok bool, err error) {
	table, known := ownerTables[subjectType]
	if !known {
		return 0, false, nil
	}
	err = sqlx.GetContext(ctx, q, &owner, `SELECT owner_id FROM `+table+` WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return owner, err == nil, err
}

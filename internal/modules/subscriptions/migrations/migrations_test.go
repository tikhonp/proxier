package migrations_test

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/migrations"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

const now = "2026-10-07T00:00:00.000Z"

type source struct{}

func (source) Name() string      { return "subscriptions" }
func (source) Migrations() fs.FS { return migrations.FS }

func open(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.Open(t, source{})
	mustExec(t, d, `INSERT INTO subs_subscriptions (id, name, title, created_at) VALUES (1, 'Family', 'Семья', ?)`, now)
	return d
}

func exec(t *testing.T, d *db.DB, q string, args ...any) error {
	t.Helper()
	_, err := d.W.ExecContext(context.Background(), q, args...)
	return err
}

func mustExec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if err := exec(t, d, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func mustFail(t *testing.T, d *db.DB, what, q string, args ...any) {
	t.Helper()
	if err := exec(t, d, q, args...); err == nil {
		t.Fatalf("%s: accepted, want a constraint error", what)
	}
}

// insertLink: subscription, name, token, lookup, state, deleted_at.
const insertLink = `INSERT INTO subs_links (subscription_id, name, token, token_lookup, state, language, created_at, deleted_at)
	VALUES (?, ?, ?, ?, ?, 'ru', '` + now + `', ?)`

func TestSubscriptionsMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	srcs := []db.MigrationSource{dbtest.Platform, source{}}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.R.GetContext(ctx, &n, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 'subs\_%' ESCAPE '\'`); err != nil || n != 7 {
		t.Fatalf("%d tables after up: %v", n, err)
	}
	if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := d.R.GetContext(ctx, &n, `SELECT count(*) FROM sqlite_master WHERE name LIKE 'subs\_%' ESCAPE '\'`); err != nil || n != 0 {
		t.Fatalf("tables left after down: %d %v", n, err)
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestLiveLinkNamesAreUnique(t *testing.T) {
	d := open(t)
	mustExec(t, d, insertLink, 1, "Mom", []byte("a"), []byte("la"), "active", nil)
	mustFail(t, d, "a second live link named Mom", insertLink, 1, "Mom", []byte("b"), []byte("lb"), "disabled", nil)
	mustExec(t, d, `UPDATE subs_links SET state = 'deleted', deleted_at = ? WHERE name = 'Mom'`, now)
	mustExec(t, d, insertLink, 1, "Mom", []byte("b"), []byte("lb"), "active", nil)
	mustExec(t, d, insertLink, 1, "Mom", []byte("c"), []byte("lc"), "deleted", now) // two deleted ones too
}

func TestLinkNeedsSubscriptionUnlessDeleted(t *testing.T) {
	d := open(t)
	mustFail(t, d, "a live link without a subscription", insertLink, nil, "Mom", []byte("a"), []byte("la"), "active", nil)
	mustFail(t, d, "a deleted state without deleted_at", insertLink, 1, "Mom", []byte("a"), []byte("la"), "deleted", nil)
	mustFail(t, d, "a live link with deleted_at", insertLink, 1, "Mom", []byte("a"), []byte("la"), "active", now)
	mustExec(t, d, insertLink, nil, "Old", nil, nil, "deleted", now)
	mustExec(t, d, insertLink, 1, "Gone", nil, nil, "deleted", now)
	mustExec(t, d, insertLink, 1, "Mom", []byte("a"), []byte("la"), "active", nil)
	mustFail(t, d, "deleting a subscription a live link points to", `DELETE FROM subs_subscriptions WHERE id = 1`)
	mustExec(t, d, `DELETE FROM subs_links WHERE name = 'Mom'`)
	mustExec(t, d, `DELETE FROM subs_subscriptions WHERE id = 1`)
	var sub sql.NullInt64
	if err := d.R.Get(&sub, `SELECT subscription_id FROM subs_links WHERE name = 'Gone'`); err != nil || sub.Valid {
		t.Fatalf("the deleted link's subscription: %v %v", sub, err)
	}
}

func TestTokenLookupIsUnique(t *testing.T) {
	d := open(t)
	mustFail(t, d, "a token without a lookup", insertLink, 1, "A", []byte("a"), nil, "active", nil)
	mustFail(t, d, "a lookup without a token", insertLink, 1, "A", nil, []byte("la"), "active", nil)
	mustExec(t, d, insertLink, 1, "A", []byte{}, []byte("la"), "active", nil) // the empty placeholder of 2b
	mustFail(t, d, "a repeated lookup", insertLink, 1, "B", []byte("b"), []byte("la"), "active", nil)
	for _, n := range []string{"C", "D", "E"} {
		mustExec(t, d, insertLink, 1, n, nil, nil, "deleted", now)
	}
}

func TestTableChecks(t *testing.T) {
	d := open(t)
	mustFail(t, d, "auto add without since", `UPDATE subs_subscriptions SET auto_add = 1`)
	mustFail(t, d, "since without auto add", `UPDATE subs_subscriptions SET auto_add_since = ?`, now)
	mustExec(t, d, `UPDATE subs_subscriptions SET auto_add = 1, auto_add_since = ?`, now)
	mustFail(t, d, "an update interval of 0", `UPDATE subs_subscriptions SET update_hours = 0`)
	mustFail(t, d, "a grace over a week", `UPDATE subs_subscriptions SET hide_grace_min = 10081`)

	mustExec(t, d, insertLink, 1, "Mom", []byte("a"), []byte("la"), "active", nil)
	mustExec(t, d, `INSERT INTO subs_cutoffs (id, link_id, created_at, created_by) VALUES (1, 1, ?, 'admin')`, now)
	item := `INSERT INTO subs_cutoff_items (cutoff_id, position, server_id, server_name, state) VALUES (1, ?, ?, ?, ?)`
	mustExec(t, d, item, 1, 1, "nl-1", "running")
	mustFail(t, d, "a second running item", item, 2, 2, "de-1", "running")
	mustExec(t, d, item, 2, 2, "de-1", "waiting")
	mustFail(t, d, "an unknown item state", item, 3, 3, "x", "paused")
}

package migrations_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/migrations"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

const now = "2026-10-08T00:00:00.000Z"

type source struct{}

func (source) Name() string      { return "routing" }
func (source) Migrations() fs.FS { return migrations.FS }

func open(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.Open(t, source{})
	mustExec(t, d, insertService, 1, "anthropic", "v2fly", "v2fly:anthropic")
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

func count(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.R.Get(&n, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// insertService: id, tag, source, selector.
const insertService = `INSERT INTO routing_services (id, tag, source, selector, created_at) VALUES (?, ?, ?, ?, '` + now + `')`

// insertSnapshot: service, status, reason, accepted_at.
const insertSnapshot = `INSERT INTO routing_snapshots (service_id, status, suffix_count, exact_count, hash, reason, fetched_at, accepted_at)
	VALUES (?, ?, 0, 0, 'h', ?, '` + now + `', ?)`

func TestRoutingMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	srcs := []db.MigrationSource{dbtest.Platform, source{}}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 'routing\_%' ESCAPE '\' AND name <> 'routing_goose_db_version'`); n != 20 {
		t.Fatalf("%d tables after up", n)
	}
	var name string
	if err := d.R.Get(&name, `SELECT name FROM routing_lists WHERE is_default = 1`); err != nil || name != "Main" {
		t.Fatalf("the default list: %q %v", name, err)
	}
	if n := count(t, d, `SELECT count(*) FROM routing_catalog_sources`); n != 4 {
		t.Fatalf("%d catalog sources", n)
	}
	if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name LIKE 'routing\_%' ESCAPE '\' AND name <> 'routing_goose_db_version'`); n != 0 {
		t.Fatalf("tables left after down: %d", n)
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM routing_lists WHERE name = 'Main' AND is_default = 1`); n != 1 {
		t.Fatalf("Main after up again: %d", n)
	}
}

func TestTableChecks(t *testing.T) {
	d := open(t)
	mustFail(t, d, "a second default list", `INSERT INTO routing_lists (name, is_default, created_at) VALUES ('Parents', 1, ?)`, now)
	mustExec(t, d, `INSERT INTO routing_lists (name, is_default, created_at) VALUES ('Parents', 0, ?)`, now)

	mustExec(t, d, insertSnapshot, 1, "accepted", "", now)
	mustFail(t, d, "a second accepted snapshot", insertSnapshot, 1, "accepted", "", now)
	mustExec(t, d, insertSnapshot, 1, "superseded", "", now)
	mustFail(t, d, "a rejected snapshot without a reason", insertSnapshot, 1, "rejected", "", nil)
	mustFail(t, d, "a rejected snapshot with accepted_at", insertSnapshot, 1, "rejected", "shrink", now)
	mustFail(t, d, "an accepted snapshot without accepted_at", insertSnapshot, 1, "superseded", "", nil)
	mustExec(t, d, insertSnapshot, 1, "rejected", "shrink", nil)
	mustFail(t, d, "a dismissed accepted snapshot", `UPDATE routing_snapshots SET dismissed_at = ? WHERE status = 'accepted'`, now)
	mustExec(t, d, `UPDATE routing_snapshots SET dismissed_at = ? WHERE status = 'rejected'`, now)
}

func TestDeletesFollowOwnership(t *testing.T) {
	d := open(t)
	mustExec(t, d, insertSnapshot, 1, "accepted", "", now)
	mustExec(t, d, insertService, 2, "mine", "custom", "")
	mustExec(t, d, `INSERT INTO routing_custom_domains (service_id, domain, exact) VALUES (2, 'example.com', 0)`)
	mustExec(t, d, insertSnapshot, 2, "accepted", "", now)
	mustExec(t, d, `INSERT INTO routing_list_services (list_id, service_id, position, added_at) VALUES (1, 1, 1, ?)`, now)
	mustExec(t, d, `INSERT INTO routing_list_services (list_id, service_id, position, added_at) VALUES (1, 2, 2, ?)`, now)

	mustFail(t, d, "deleting a service in a list", `DELETE FROM routing_services WHERE id = 1`)
	mustExec(t, d, `INSERT INTO routing_lists (id, name, created_at) VALUES (2, 'Parents', ?)`, now)
	mustExec(t, d, `INSERT INTO routing_list_services (list_id, service_id, position, added_at) VALUES (2, 1, 1, ?)`, now)
	mustExec(t, d, `DELETE FROM routing_lists WHERE id = 2`)
	if n := count(t, d, `SELECT count(*) FROM routing_list_services WHERE list_id = 2`); n != 0 {
		t.Fatalf("memberships of a deleted list: %d", n)
	}
	mustExec(t, d, `DELETE FROM routing_list_services WHERE service_id = 2`)
	mustExec(t, d, `DELETE FROM routing_services WHERE id = 2`)
	if n := count(t, d, `SELECT count(*) FROM routing_snapshots WHERE service_id = 2`) +
		count(t, d, `SELECT count(*) FROM routing_custom_domains WHERE service_id = 2`); n != 0 {
		t.Fatalf("rows of a deleted service: %d", n)
	}
}

func TestServiceChecks(t *testing.T) {
	d := open(t)
	mustFail(t, d, "two services with one tag", insertService, 2, "anthropic", "iplist", "iplist:anthropic")
	mustFail(t, d, "a custom service with a selector", insertService, 2, "mine", "custom", "v2fly:mine")
	mustFail(t, d, "an upstream service without a selector", insertService, 2, "mine", "v2fly", "")
	mustFail(t, d, "an unknown source", insertService, 2, "mine", "git", "git:mine")
	mustExec(t, d, insertService, 2, "mine", "custom", "")
}

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

// insertService: id, tag, source, selector; a custom one is member 1's, an
// upstream one shared (owner 0).
const insertService = `INSERT INTO routing_services (id, tag, source, selector, created_at, owner_id)
	VALUES (?1, ?2, ?3, ?4, '` + now + `', CASE ?3 WHEN 'custom' THEN 1 ELSE 0 END)`

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
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('routing_imports') WHERE name = 'list_failures'`); n != 1 {
		t.Fatalf("00002: list_failures %d", n)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND p.name = 'owner_id'`); n != 7 {
		t.Fatalf("00003: %d tables with an owner", n)
	}
	if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatalf("down 00003: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND p.name = 'owner_id'`); n != 0 {
		t.Fatalf("owners after down: %d", n)
	}
	if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatalf("down 00002: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('routing_imports') WHERE name = 'list_failures'`); n != 0 {
		t.Fatalf("list_failures after down: %d", n)
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

func TestThingsFromBeforeMembersBelongToMemberOne(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO routing_services (id, tag, source, selector, created_at) VALUES (2, 'mine', 'custom', '', ?)`, now)
	mustExec(t, d, `INSERT INTO routing_shadowrocket (name, list_id, token, token_lookup, created_at) VALUES ('phone', 1, X'00', X'01', ?)`, now)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT owner_id FROM routing_services WHERE tag = 'anthropic'`); n != 0 {
		t.Errorf("an upstream service has owner %d, want shared (0)", n)
	}
	if n := count(t, d, `SELECT owner_id FROM routing_services WHERE tag = 'mine'`); n != 1 {
		t.Errorf("a custom service has owner %d, want 1", n)
	}
	if n := count(t, d, `SELECT count(*) FROM routing_lists WHERE owner_id = 1`) + count(t, d, `SELECT count(*) FROM routing_shadowrocket WHERE owner_id = 1`); n != 2 {
		t.Errorf("%d of Main and the config are member 1's", n)
	}
}

func TestOwnerChecks(t *testing.T) {
	d := open(t)
	mustFail(t, d, "a custom service without an owner", `INSERT INTO routing_services (id, tag, source, selector, created_at, owner_id) VALUES (2, 'mine', 'custom', '', ?, 0)`, now)
	mustFail(t, d, "an upstream service with an owner", `INSERT INTO routing_services (id, tag, source, selector, created_at, owner_id) VALUES (2, 'yt', 'v2fly', 'v2fly:yt', ?, 3)`, now)
	mustFail(t, d, "a shared service given an owner", `UPDATE routing_services SET owner_id = 2 WHERE id = 1`)
	mustExec(t, d, `INSERT INTO routing_services (id, tag, source, selector, created_at, owner_id) VALUES (2, 'mine', 'custom', '', ?, 2)`, now)
	mustExec(t, d, `UPDATE routing_services SET owner_id = 3 WHERE id = 2`)
	mustFail(t, d, "a custom service losing its owner", `UPDATE routing_services SET owner_id = 0 WHERE id = 2`)
	for _, table := range []string{"routing_lists", "routing_routers", "routing_shadowrocket", "routing_imports", "routing_discovery_runs"} {
		if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name = ? AND sql LIKE '%owner_id INTEGER NOT NULL DEFAULT 1 CHECK (owner_id > 0)%'`, table); n != 1 {
			t.Errorf("%s doesn't check its owner", table)
		}
	}
	mustFail(t, d, "Main of no member", `UPDATE routing_lists SET owner_id = 0`)
	mustFail(t, d, "a list of no member", `INSERT INTO routing_lists (name, created_at, owner_id) VALUES ('Parents', ?, 0)`, now)
}

func TestServiceChecks(t *testing.T) {
	d := open(t)
	mustFail(t, d, "two services with one tag", insertService, 2, "anthropic", "iplist", "iplist:anthropic")
	mustFail(t, d, "a custom service with a selector", insertService, 2, "mine", "custom", "v2fly:mine")
	mustFail(t, d, "an upstream service without a selector", insertService, 2, "mine", "v2fly", "")
	mustFail(t, d, "an unknown source", insertService, 2, "mine", "git", "git:mine")
	mustExec(t, d, insertService, 2, "mine", "custom", "")
}

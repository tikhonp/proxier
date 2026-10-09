package migrations_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/migrations"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

const now = "2026-10-09T00:00:00.000Z"

type source struct{}

func (source) Name() string      { return "routerscripts" }
func (source) Migrations() fs.FS { return migrations.FS }

func exec(d *db.DB, q string, args ...any) error {
	_, err := d.W.ExecContext(context.Background(), q, args...)
	return err
}

func mustExec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if err := exec(d, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func mustFail(t *testing.T, d *db.DB, what, q string, args ...any) {
	t.Helper()
	if err := exec(d, q, args...); err == nil {
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

// open gives a database with script 1 holding versions 1 and 2.
func open(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.Open(t, source{})
	mustExec(t, d, `INSERT INTO rscripts_scripts (id, name, slug, created_at) VALUES (1, 'fresh-router', 'fresh-router', ?)`, now)
	for _, n := range []int{1, 2} {
		mustExec(t, d, insertVersion, 1, n)
	}
	return d
}

const insertVersion = `INSERT INTO rscripts_versions (script_id, number, body, sha256, published_at, published_by) VALUES (?, ?, 'x', 'h', '` + now + `', 'admin')`

const insertGeneration = `INSERT INTO rscripts_generations (id, script_id, version, router_name, file_name, created_at, created_by)
	VALUES (?, ?, ?, 'Dacha', 'fresh-router-Dacha-v1.rsc', '` + now + `', 'admin')`

func TestRouterScriptsMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	srcs := []db.MigrationSource{dbtest.Platform, source{}}
	tables := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 'rscripts\_%' ESCAPE '\'`
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, tables); n != 5 {
		t.Fatalf("%d tables after up", n)
	}
	if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := count(t, d, tables); n != 0 {
		t.Fatalf("tables left after down: %d", n)
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := count(t, d, tables); n != 5 {
		t.Fatalf("%d tables after up again", n)
	}
}

func TestCurrentVersionMustExist(t *testing.T) {
	d := open(t)
	mustFail(t, d, "a current version that doesn't exist", `UPDATE rscripts_scripts SET current_version = 3 WHERE id = 1`)
	mustExec(t, d, `UPDATE rscripts_scripts SET current_version = 2 WHERE id = 1`)
	mustExec(t, d, `UPDATE rscripts_scripts SET current_version = NULL WHERE id = 1`)
	// another script's version doesn't count
	mustExec(t, d, `INSERT INTO rscripts_scripts (id, name, slug, created_at) VALUES (2, 'other', 'other', ?)`, now)
	mustFail(t, d, "another script's version", `UPDATE rscripts_scripts SET current_version = 1 WHERE id = 2`)
	mustFail(t, d, "version 0", insertVersion, 2, 0)
	// inserting the version after pointing at it in one transaction works (deferred)
	tx := d.W.MustBegin()
	if _, err := tx.Exec(`UPDATE rscripts_scripts SET current_version = 1 WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(insertVersion, 2, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("deferred reference: %v", err)
	}
	mustExec(t, d, `UPDATE rscripts_scripts SET current_version = 2 WHERE id = 1`)
	mustExec(t, d, `INSERT INTO rscripts_drafts (script_id, body, based_on, updated_at, updated_by) VALUES (1, 'x', 2, ?, 'admin')`, now)
	mustFail(t, d, "a second draft", `INSERT INTO rscripts_drafts (script_id, body, updated_at, updated_by) VALUES (1, 'y', ?, 'admin')`, now)
	mustFail(t, d, "revision 0", `INSERT INTO rscripts_drafts (script_id, body, revision, updated_at, updated_by) VALUES (2, 'y', 0, ?, 'admin')`, now)
	mustExec(t, d, `DELETE FROM rscripts_scripts WHERE id = 1`)
	if n := count(t, d, `SELECT count(*) FROM rscripts_versions WHERE script_id = 1`) + count(t, d, `SELECT count(*) FROM rscripts_drafts WHERE script_id = 1`); n != 0 {
		t.Errorf("rows of a deleted script: %d", n)
	}
}

func TestGenerationsHoldTheirScript(t *testing.T) {
	d := open(t)
	mustFail(t, d, "a generation of a version that doesn't exist", insertGeneration, 1, 1, 3)
	mustExec(t, d, insertGeneration, 1, 1, 1)
	mustFail(t, d, "link_created without a link", `UPDATE rscripts_generations SET link_created = 1 WHERE id = 1`)
	mustExec(t, d, `UPDATE rscripts_generations SET link_created = 1, link_id = 7 WHERE id = 1`)
	mustFail(t, d, "router_registered without a router", `UPDATE rscripts_generations SET router_registered = 1 WHERE id = 1`)
	mustExec(t, d, `UPDATE rscripts_generations SET router_registered = 1, router_id = 3 WHERE id = 1`)
	mustFail(t, d, "deleting a script with a generation", `DELETE FROM rscripts_scripts WHERE id = 1`)
	if n := count(t, d, `SELECT count(*) FROM rscripts_versions WHERE script_id = 1`); n != 2 {
		t.Errorf("versions after a refused delete: %d", n)
	}
	mustExec(t, d, `DELETE FROM rscripts_generations WHERE id = 1`)
	mustExec(t, d, `DELETE FROM rscripts_scripts WHERE id = 1`)
}

func TestFetchURLChecks(t *testing.T) {
	d := open(t)
	mustExec(t, d, insertGeneration, 1, 1, 1)
	ins := `INSERT INTO rscripts_fetch_urls (id, generation_id, token, token_lookup, state, created_at, created_by, expires_at, ended_at)
		VALUES (?, 1, ?, ?, ?, '` + now + `', 'admin', '` + now + `', ?)`
	mustFail(t, d, "a token without its lookup", ins, 1, []byte("t"), nil, "waiting", nil)
	mustFail(t, d, "a lookup without its token", ins, 1, nil, []byte("l"), "waiting", nil)
	mustFail(t, d, "waiting without a token", ins, 1, nil, nil, "waiting", nil)
	mustFail(t, d, "used with a token", ins, 1, []byte("t"), []byte("l"), "used", now)
	mustFail(t, d, "waiting with ended_at", ins, 1, []byte("t"), []byte("l"), "waiting", now)
	mustFail(t, d, "used without ended_at", ins, 1, nil, nil, "used", nil)
	mustFail(t, d, "an unknown state", ins, 1, nil, nil, "gone", now)
	mustExec(t, d, ins, 1, []byte(""), []byte("l1"), "waiting", nil)
	mustFail(t, d, "a second waiting URL", ins, 2, []byte("t"), []byte("l2"), "waiting", nil)
	mustExec(t, d, `UPDATE rscripts_fetch_urls SET state = 'replaced', token = NULL, token_lookup = NULL, ended_at = ? WHERE id = 1`, now)
	mustExec(t, d, ins, 2, []byte("t"), []byte("l2"), "waiting", nil)
	mustExec(t, d, `INSERT INTO rscripts_generations (id, script_id, version, router_name, file_name, created_at, created_by)
		VALUES (2, 1, 2, 'Parents', 'x', ?, 'admin')`, now)
	insGen2 := `INSERT INTO rscripts_fetch_urls (id, generation_id, token, token_lookup, state, created_at, created_by, expires_at)
		VALUES (3, 2, ?, ?, 'waiting', '` + now + `', 'admin', '` + now + `')`
	mustFail(t, d, "a lookup used twice", insGen2, []byte("t"), []byte("l2"))
	mustExec(t, d, `UPDATE rscripts_fetch_urls SET state = 'used', token = NULL, token_lookup = NULL, ended_at = ? WHERE id = 2`, now)
	mustExec(t, d, insGen2, []byte("t"), []byte("l2"))
	mustFail(t, d, "a fetch URL of no generation", `INSERT INTO rscripts_fetch_urls (generation_id, state, created_at, created_by, expires_at, ended_at)
		VALUES (9, 'expired', ?, 'admin', ?, ?)`, now, now, now)
}

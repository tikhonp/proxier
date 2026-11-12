package migrations_test

import (
	"context"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/migrations"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

const now = "2026-10-07T00:00:00.000Z"

type source struct{}

func (source) Name() string      { return "servers" }
func (source) Migrations() fs.FS { return migrations.FS }

func open(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.Open(t, source{})
	mustExec(t, d, `INSERT INTO servers_templates (id, slug, name, created_at) VALUES (1, 't', 'T', ?)`, now)
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

const insertServer = `INSERT INTO servers_servers (country, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, health, health_since, created_at)
	VALUES ('NL', ?, ?, ?, 'h', 'h', ?, 1, 1, ?, ?, ?)`

func TestServersMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	srcs := []db.MigrationSource{dbtest.Platform, source{}}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatal(err)
	}
	for range 11 {
		if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	var n int
	if err := d.R.GetContext(ctx, &n, `SELECT count(*) FROM sqlite_master WHERE name LIKE 'servers\_%' ESCAPE '\' AND name NOT LIKE '%goose%'`); err != nil || n != 0 {
		t.Fatalf("tables left after down: %d %v", n, err)
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, srcs...); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestOneSSHPortAgain(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, dbtest.Platform, upTo{3}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO servers_templates (id, slug, name, created_at) VALUES (1, 't', 'T', ?)`, now)
	for _, s := range []struct {
		name, state, step string
		port, setup       int
	}{
		{"nl-1", "failed", "install-access", 23817, 22},
		{"nl-2", "failed", "preflight", 23818, 2222},
		{"nl-3", "active", "", 23819, 22},
		{"nl-4", "failed", "dns", 23820, 22},
	} {
		var health any
		if s.state == "active" {
			health = "healthy"
		}
		mustExec(t, d, `INSERT INTO servers_servers (country, number, name, ip, ssh_port, setup_ssh_port, management_hostname, proxy_hostname, state, failed_step, health, health_since, template_id, template_version, created_at)
			VALUES ('NL', ?, ?, ?, ?, ?, 'h', 'h', ?, NULLIF(?, ''), ?, ?, 1, 1, ?)`,
			s.name[3:], s.name, "203.0.113."+s.name[3:], s.port, s.setup, s.state, s.step, health, now, now)
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, upTo{4}); err != nil {
		t.Fatal(err)
	}
	ports := func() map[string]int {
		t.Helper()
		var rows []struct {
			Name string `db:"name"`
			Port int    `db:"ssh_port"`
		}
		if err := d.R.SelectContext(ctx, &rows, `SELECT name, ssh_port FROM servers_servers`); err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, r := range rows {
			got[r.Name] = r.Port
		}
		return got
	}
	// Only a server that failed before base bootstrap never had sshd moved.
	want := map[string]int{"nl-1": 22, "nl-2": 2222, "nl-3": 23819, "nl-4": 23820}
	if got := ports(); !maps.Equal(got, want) {
		t.Fatalf("ssh ports %v, want %v", got, want)
	}

	if err := db.MigrateDown(ctx, d, dbtest.Discard, upTo{4}); err != nil {
		t.Fatalf("down: %v", err)
	}
	var n int
	if err := d.R.GetContext(ctx, &n, `SELECT count(*) FROM servers_servers WHERE setup_ssh_port != ssh_port`); err != nil || n != 0 {
		t.Fatalf("%d servers with two ports after down, %v", n, err)
	}
}

func TestLiveIPIsUnique(t *testing.T) {
	d := open(t)
	mustExec(t, d, insertServer, 1, "nl-1", "203.0.113.1", "provisioning", nil, nil, now)
	mustFail(t, d, "a second live server with the same IP", insertServer, 2, "nl-2", "203.0.113.1", "provisioning", nil, nil, now)
	mustFail(t, d, "the same IP in a failed server", insertServer, 2, "nl-2", "203.0.113.1", "failed", nil, nil, now)
	// once retired, the IP is free again, and a retired one never blocks
	mustExec(t, d, `UPDATE servers_servers SET state = 'retired', retired_at = ? WHERE name = 'nl-1'`, now)
	mustExec(t, d, insertServer, 2, "nl-2", "203.0.113.1", "provisioning", nil, nil, now)
	mustExec(t, d, insertServer, 3, "nl-3", "203.0.113.1", "retired", nil, nil, now)
	// the same name twice; the same number in a country is fine (nl-1 and a
	// location's ams-1 were both in NL before servers had a country)
	mustExec(t, d, insertServer, 2, "ams-2", "203.0.113.9", "provisioning", nil, nil, now)
	mustFail(t, d, "a repeated name", insertServer, 9, "nl-2", "203.0.113.9", "provisioning", nil, nil, now)
}

func TestHealthOnlyWhenActive(t *testing.T) {
	d := open(t)
	mustFail(t, d, "an active server without health", insertServer, 1, "nl-1", "203.0.113.1", "active", nil, nil, now)
	mustFail(t, d, "a provisioning server with health", insertServer, 1, "nl-1", "203.0.113.1", "provisioning", "healthy", now, now)
	mustFail(t, d, "a failed server with health", insertServer, 1, "nl-1", "203.0.113.1", "failed", "down", now, now)
	mustFail(t, d, "an unknown health state", insertServer, 1, "nl-1", "203.0.113.1", "active", "great", now, now)
	mustFail(t, d, "an unknown lifecycle state", insertServer, 1, "nl-1", "203.0.113.1", "sleeping", nil, nil, now)
	mustExec(t, d, insertServer, 1, "nl-1", "203.0.113.1", "active", "healthy", now, now)
	// leaving active takes the health away with it
	mustFail(t, d, "retiring an active server and keeping its health", `UPDATE servers_servers SET state = 'retired'`)
	mustExec(t, d, `UPDATE servers_servers SET state = 'retired', health = NULL`)
}

func TestOneOpenAgentSessionPerTemplate(t *testing.T) {
	d := open(t)
	ins := `INSERT INTO servers_agent_sessions (template_id, token_lookup, problem, agent, opened_at, expires_at, closed_at, close_reason) VALUES (1, ?, 'p', 'claude-code', ?, ?, ?, ?)`
	mustExec(t, d, ins, []byte("a"), now, now, nil, nil)
	mustFail(t, d, "a second open session", ins, []byte("b"), now, now, nil, nil)
	mustFail(t, d, "a repeated token lookup", ins, []byte("a"), now, now, now, "expired")
	// a closed one does not count, and a template may have many closed ones
	mustExec(t, d, ins, []byte("c"), now, now, now, "revoked")
	mustExec(t, d, ins, []byte("d"), now, now, now, "expired")
	mustFail(t, d, "an unknown close reason", ins, []byte("e"), now, now, now, "vanished")
	// after closing, a new one may open
	mustExec(t, d, `UPDATE servers_agent_sessions SET closed_at = ?, close_reason = 'published' WHERE token_lookup = ?`, now, []byte("a"))
	mustExec(t, d, ins, []byte("f"), now, now, nil, nil)
}

func TestCountryAndTemplateConstraints(t *testing.T) {
	d := open(t)
	ins := `INSERT INTO servers_servers (country, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at)
		VALUES (?, 1, ?, '203.0.113.1', 'h', 'h', 'retired', 1, 1, ?)`
	mustFail(t, d, "a lower-case country", ins, "nl", "nl-1", now)
	mustFail(t, d, "a country of three letters", ins, "NLD", "nl-1", now)
	mustFail(t, d, "an empty country", ins, "", "nl-1", now)
	mustExec(t, d, ins, "NL", "nl-1", now)
	mustFail(t, d, "a duplicate slug", `INSERT INTO servers_templates (slug, name, created_at) VALUES ('t', 'x', ?)`, now)
	mustExec(t, d, `INSERT INTO servers_templates (id, slug, name, created_at) VALUES (2, 't2', 'T', ?)`, now)
	mustExec(t, d, `INSERT INTO servers_template_versions (id, template_id, number, published_at, published_by) VALUES (1, 2, 1, ?, 'system')`, now)
	mustFail(t, d, "a repeated version number", `INSERT INTO servers_template_versions (template_id, number, published_at, published_by) VALUES (2, 1, ?, 'system')`, now)
	mustFail(t, d, "version number 0", `INSERT INTO servers_template_versions (template_id, number, published_at, published_by) VALUES (2, 0, ?, 'system')`, now)
	// deleting a template takes its versions and drafts along
	mustExec(t, d, `INSERT INTO servers_template_files (version_id, path, content) VALUES (1, 'a', x'00')`)
	mustExec(t, d, `INSERT INTO servers_template_drafts (template_id, updated_at, updated_by) VALUES (2, ?, 'admin')`, now)
	mustExec(t, d, `INSERT INTO servers_draft_files (template_id, path, content) VALUES (2, 'a', x'00')`)
	mustExec(t, d, `DELETE FROM servers_templates WHERE id = 2`)
	for _, tbl := range []string{"servers_template_versions", "servers_template_files", "servers_template_drafts", "servers_draft_files"} {
		var n int
		if err := d.R.GetContext(context.Background(), &n, `SELECT count(*) FROM `+tbl); err != nil || n != 0 {
			t.Errorf("%s: %d rows left, %v", tbl, n, err)
		}
	}
	// a server keeps its template
	mustFail(t, d, "deleting a template with a server", `DELETE FROM servers_templates WHERE id = 1`)
}

func TestPreviousIPConstraints(t *testing.T) {
	d := open(t)
	mustExec(t, d, insertServer, 1, "nl-1", "203.0.113.1", "active", "healthy", now, now)
	mustExec(t, d, insertServer, 2, "nl-2", "203.0.113.2", "active", "healthy", now, now)
	const ins = `INSERT INTO servers_previous_ips (server_id, ip, since, state, state_since) VALUES (?, ?, ?, ?, ?)`
	mustExec(t, d, ins, 1, "198.51.100.1", now, "unknown", now)
	mustFail(t, d, "one IP kept by two servers", ins, 2, "198.51.100.1", now, "unknown", now)
	mustFail(t, d, "a state that isn't one", ins, 2, "198.51.100.2", now, "blocked", now)
	mustFail(t, d, "a server that doesn't exist", ins, 99, "198.51.100.3", now, "unknown", now)
	mustExec(t, d, `INSERT INTO servers_previous_ip_results (previous_ip_id, at, ok, class) VALUES (1, ?, 0, 'stalled')`, now)
	mustFail(t, d, "a result of no previous IP", `INSERT INTO servers_previous_ip_results (previous_ip_id, at, ok) VALUES (99, ?, 1)`, now)
	// forgetting the IP takes its results with it
	mustExec(t, d, `DELETE FROM servers_previous_ips WHERE id = 1`)
	var n int
	if err := d.R.Get(&n, `SELECT count(*) FROM servers_previous_ip_results`); err != nil || n != 0 {
		t.Fatalf("results left: %d %v", n, err)
	}
}

// An install that still has the pristine seed gets a new default version that
// supports Ubuntu 26.04; a template of the member's own stays untouched.
func TestSeedSupportsUbuntu26(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, dbtest.Platform, upTo{8}); err != nil {
		t.Fatal(err)
	}
	const oldManifest = "name: Seed\nrequires:\n  os: [debian-12, debian-13, ubuntu-22.04, ubuntu-24.04]\n  arch: [amd64]\n"
	for _, tpl := range []struct {
		id         int64
		slug, name string
	}{
		{1, "vless-xhttp", "Seed"},
		{2, "mine", "Mine"},
	} {
		mustExec(t, d, `INSERT INTO servers_templates (id, slug, name, default_version, created_at) VALUES (?, ?, ?, 1, ?)`,
			tpl.id, tpl.slug, tpl.name, now)
		mustExec(t, d, `INSERT INTO servers_template_versions (id, template_id, number, notes, warnings, source, published_at, published_by)
			VALUES (?, ?, 1, '', '[]', '{}', ?, 'system')`, tpl.id, tpl.id, now)
		mustExec(t, d, `INSERT INTO servers_template_files (version_id, path, content) VALUES (?, 'manifest.yaml', ?), (?, 'compose.yaml', ?)`,
			tpl.id, []byte(oldManifest), tpl.id, []byte("services: {}\n"))
	}

	if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatal(err)
	}

	var def int
	if err := d.R.GetContext(ctx, &def, `SELECT default_version FROM servers_templates WHERE slug = 'vless-xhttp'`); err != nil || def != 2 {
		t.Fatalf("default version %d, %v", def, err)
	}
	var manifest string
	if err := d.R.GetContext(ctx, &manifest, `
		SELECT f.content FROM servers_template_files f
		JOIN servers_template_versions v ON v.id = f.version_id
		WHERE v.template_id = 1 AND v.number = 2 AND f.path = 'manifest.yaml'`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manifest, "os: [debian-12+, ubuntu-22.04+]") || strings.Contains(manifest, "ubuntu-24.04") {
		t.Fatalf("manifest:\n%s", manifest)
	}
	var compose string
	if err := d.R.GetContext(ctx, &compose, `
		SELECT f.content FROM servers_template_files f
		JOIN servers_template_versions v ON v.id = f.version_id
		WHERE v.template_id = 1 AND v.number = 2 AND f.path = 'compose.yaml'`); err != nil || compose != "services: {}\n" {
		t.Fatalf("compose %q, %v", compose, err)
	}

	var mine int
	if err := d.R.GetContext(ctx, &mine, `
		SELECT count(*) FROM servers_template_versions WHERE template_id = 2`); err != nil || mine != 1 {
		t.Fatalf("the member's template got %d versions, %v", mine, err)
	}
	if err := d.R.GetContext(ctx, &def, `SELECT default_version FROM servers_templates WHERE id = 2`); err != nil || def != 1 {
		t.Fatalf("the member's default version %d, %v", def, err)
	}

	t.Run("rollback and reapply preserves the published files", func(t *testing.T) {
		type file struct {
			VersionID int64 `db:"version_id"`
			Path      string
			Content   string
		}
		const query = `SELECT f.version_id, f.path, CAST(f.content AS TEXT) AS content
			FROM servers_template_files f
			JOIN servers_template_versions v ON v.id = f.version_id
			WHERE v.template_id = 1 AND v.number = 2 ORDER BY f.path`
		var before, after []file
		if err := d.R.SelectContext(ctx, &before, query); err != nil {
			t.Fatal(err)
		}
		if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
			t.Fatalf("down: %v", err)
		}
		if err := d.R.SelectContext(ctx, &after, query); err != nil || !slices.Equal(before, after) {
			t.Fatalf("rollback changed the published files: %v, %v", after, err)
		}
		if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
			t.Fatalf("up again: %v", err)
		}
		if err := d.R.SelectContext(ctx, &after, query); err != nil || !slices.Equal(before, after) {
			t.Fatalf("reapplying changed the published files: %v, %v", after, err)
		}
		if err := d.R.GetContext(ctx, &def, `SELECT default_version FROM servers_templates WHERE id = 1`); err != nil || def != 2 {
			t.Fatalf("default version after reapplying %d, %v", def, err)
		}
	})
}

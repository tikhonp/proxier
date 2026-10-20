package migrations_test

import (
	"context"
	"io/fs"
	"maps"
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
	for range 4 {
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
	if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
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

	if err := db.MigrateDown(ctx, d, dbtest.Discard, source{}); err != nil {
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

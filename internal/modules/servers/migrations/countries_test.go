package migrations_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tikhonp/proxier/internal/modules/servers/migrations"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

// upTo is the module with only its first migrations, under the same version
// table: upTo{1} as deployed before servers had a country, upTo{2} with the
// countries migration as the last one.
type upTo struct{ n int }

func (upTo) Name() string { return "servers" }
func (u upTo) Migrations() fs.FS {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		panic(err)
	}
	out := fstest.MapFS{}
	for _, name := range names[:u.n] {
		b, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			panic(err)
		}
		out[name] = &fstest.MapFile{Data: b}
	}
	return out
}

// beforeCountries is a database shaped like production before the change:
// locations with the country's name, a custom name, a code that isn't a
// country's, one without servers, one in a withdrawn code with a successor
// (UK, which the picker offered as "United Kingdom" beside GB) and one
// without (SU); servers in every lifecycle state with the
// rows that hang off them; and a server id the sequence has gone past.
func beforeCountries(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, dbtest.Platform, upTo{1}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO servers_templates (id, slug, name, created_at) VALUES (1, 't', 'T', ?)`, now)
	for _, l := range [][]any{
		{1, "nl", "Netherlands", "NL", 3},
		{2, "ams", "Амстердам", "NL", 1},
		{3, "de", "Germany", "DE", 0},
		{4, "uk", "Britain", "UK", 2},
		{5, "su", "Old", "SU", 1},
	} {
		mustExec(t, d, `INSERT INTO servers_locations (id, code, name, country, last_number, created_at) VALUES (?, ?, ?, ?, ?, ?)`, append(l, now)...)
	}
	ins := `INSERT INTO servers_servers (id, location_id, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, health, health_since, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?)`
	for _, s := range [][]any{
		{1, 1, 1, "nl-1", "203.0.113.1", "nl-1.h.example", "nl-1.h.example", "retired", nil, nil, "old"},
		{2, 1, 2, "nl-2", "203.0.113.2", "nl-2.h.example", "nl-2.h.example", "failed", nil, nil, ""},
		{3, 1, 3, "nl-3", "203.0.113.3", "nl-3.h.example", "nl-3.h.example", "active", "healthy", now, ""},
		{4, 2, 1, "ams-1", "203.0.113.4", "ams-1.h.example", "ams-1.h.example", "active", "blocked", now, ""},
		{5, 4, 1, "uk-1", "203.0.113.5", "uk-1.h.example", "uk-1.h.example", "retired", nil, nil, ""},
		{6, 4, 2, "uk-2", "203.0.113.6", "uk-2.h.example", "uk-2.h.example", "provisioning", nil, nil, ""},
		{7, 5, 1, "su-1", "203.0.113.10", "su-1.h.example", "su-1.h.example", "retired", nil, nil, ""},
		{9, 4, 3, "uk-3", "203.0.113.9", "uk-3.h.example", "uk-3.h.example", "provisioning", nil, nil, ""},
	} {
		mustExec(t, d, ins, append(s, now)...)
	}
	// uk-3 was never kept (only a test does that), but its id was used
	mustExec(t, d, `DELETE FROM servers_servers WHERE id = 9`)
	mustExec(t, d, `UPDATE servers_locations SET last_number = 3 WHERE id = 4`)
	for _, id := range []int{1, 3, 4} {
		mustExec(t, d, `INSERT INTO servers_generated_values (server_id, key, value, created_at) VALUES (?, 'uuid', x'01', ?)`, id, now)
		mustExec(t, d, `INSERT INTO servers_endpoints (server_id, key, type, host, port, secret, display_name, position, updated_at)
			VALUES (?, 'main', 'vless-xhttp-tls', 'h', 443, x'01', 'shown', 0, ?)`, id, now)
		mustExec(t, d, `INSERT INTO servers_deployments (server_id, kind, template_version, state, started_at) VALUES (?, 'provision', 1, 'succeeded', ?)`, id, now)
	}
	return d
}

func count(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.R.GetContext(context.Background(), &n, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func foreignKeysOn(t *testing.T, d *db.DB) {
	t.Helper()
	var on int
	if err := d.W.GetContext(context.Background(), &on, `PRAGMA foreign_keys`); err != nil || on != 1 {
		t.Fatalf("foreign keys on the write connection: %d %v", on, err)
	}
}

// Each location's country goes onto its servers and the locations table goes,
// without losing a server, a name, a row that hangs off a server or an id.
func TestLocationsBecomeTheirServersCountries(t *testing.T) {
	ctx := context.Background()
	d := beforeCountries(t)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatal(err)
	}
	foreignKeysOn(t, d)
	var got []struct {
		ID      int64
		Country string
		Number  int
		Name    string
		State   string
		Health  *string
		Notes   string
	}
	if err := d.R.SelectContext(ctx, &got, `SELECT id, country, number, name, state, health, notes FROM servers_servers ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"nl-1": "NL", "nl-2": "NL", "nl-3": "NL", "ams-1": "NL", "uk-1": "GB", "uk-2": "GB", "su-1": "SU"}
	if len(got) != len(want) {
		t.Fatalf("servers after: %+v", got)
	}
	for _, s := range got {
		if want[s.Name] != s.Country {
			t.Errorf("%s: country %q, want %q", s.Name, s.Country, want[s.Name])
		}
	}
	if got[0].Notes != "old" || got[2].Health == nil || *got[2].Health != "healthy" || got[3].Number != 1 {
		t.Errorf("columns not carried over: %+v", got)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name = 'servers_locations'`); n != 0 {
		t.Error("servers_locations is still there")
	}
	for _, tbl := range []string{"servers_generated_values", "servers_endpoints", "servers_deployments"} {
		if n := count(t, d, `SELECT count(*) FROM `+tbl); n != 3 {
			t.Errorf("%s: %d rows, want 3", tbl, n)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("%d broken references", n)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name = 'servers_servers_live_ip'`); n != 1 {
		t.Error("the live-IP index is gone")
	}
	// the archive holds every location as the admin entered it
	var raw string
	if err := d.R.GetContext(ctx, &raw, `SELECT value FROM servers_meta WHERE key = 'locations.archive'`); err != nil {
		t.Fatal(err)
	}
	var archive []struct {
		Code, Name string
	}
	if err := json.Unmarshal([]byte(raw), &archive); err != nil || len(archive) != 5 || archive[1].Name != "Амстердам" {
		t.Errorf("archive %s: %v", raw, err)
	}
	// an id is never reused: the next server comes after the forgotten uk-3
	res, err := d.W.ExecContext(ctx, `INSERT INTO servers_servers (country, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at)
		VALUES ('SE', 1, 'se-1', '203.0.113.7', 'h', 'h', 'provisioning', 1, 1, ?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 10 {
		t.Errorf("new server id %d, want 10", id)
	}
	// an endpoint still needs its server
	mustFail(t, d, "an endpoint of no server", `INSERT INTO servers_endpoints (server_id, key, type, host, port, secret, display_name, position, updated_at)
		VALUES (99, 'main', 't', 'h', 443, x'01', 'x', 0, ?)`, now)
}

// Down gives back the locations as they were, adds one for a prefix first
// used since (named by its country code), and puts every server back in its
// location; Up again works.
func TestCountriesRollBackToLocations(t *testing.T) {
	ctx := context.Background()
	d := beforeCountries(t)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, upTo{2}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO servers_servers (country, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at)
		VALUES ('SE', 1, 'se-1', '203.0.113.7', 'h', 'h', 'provisioning', 1, 1, ?)`, now)
	mustExec(t, d, `INSERT INTO servers_servers (country, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at)
		VALUES ('NL', 4, 'nl-4', '203.0.113.8', 'h', 'h', 'provisioning', 1, 1, ?)`, now)
	if err := db.MigrateDown(ctx, d, dbtest.Discard, upTo{2}); err != nil {
		t.Fatalf("down: %v", err)
	}
	foreignKeysOn(t, d)
	var locs []struct {
		ID         int64
		Code, Name string
		Country    string
		LastNumber int `db:"last_number"`
	}
	if err := d.R.SelectContext(ctx, &locs, `SELECT id, code, name, country, last_number FROM servers_locations ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range locs {
		b.WriteString(strings.Join([]string{l.Code, l.Name, l.Country}, "/"))
		b.WriteString(" ")
		b.WriteString(string(rune('0' + l.LastNumber)))
		b.WriteString("; ")
	}
	if got := b.String(); got != "nl/Netherlands/NL 4; ams/Амстердам/NL 1; de/Germany/DE 0; uk/Britain/UK 3; su/Old/SU 1; se/SE/SE 1; " {
		t.Errorf("locations after down: %s", got)
	}
	if n := count(t, d, `SELECT count(*) FROM servers_servers s JOIN servers_locations l ON l.id = s.location_id
		WHERE s.name = l.code || '-' || s.number`); n != 9 {
		t.Errorf("%d of 9 servers back in their location", n)
	}
	if n := count(t, d, `SELECT count(*) FROM servers_meta WHERE key = 'locations.archive'`); n != 0 {
		t.Error("the archive is left after down")
	}
	if n := count(t, d, `SELECT count(*) FROM servers_endpoints`); n != 3 {
		t.Errorf("endpoints after down: %d", n)
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM servers_servers WHERE country = 'SE' AND name = 'se-1'`); n != 1 {
		t.Error("se-1 lost its country on the way back up")
	}
}

// A database created after the change goes down and up again with no
// locations at all.
func TestCountriesMigrationOnAnEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, dbtest.Platform, upTo{2}); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateDown(ctx, d, dbtest.Discard, upTo{2}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM servers_locations`); n != 0 {
		t.Errorf("%d locations from nothing", n)
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatal(err)
	}
	foreignKeysOn(t, d)
}

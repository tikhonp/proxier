// Package storetest gives the module's tests a migrated database with the
// module's event types declared. Only tests import it.
package storetest

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"strconv"
	"testing"

	"bytes"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/migrations"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

type source struct{}

func (source) Name() string      { return "servers" }
func (source) Migrations() fs.FS { return migrations.FS }

// Source is the module's migration source.
var Source db.MigrationSource = source{}

// Env is a migrated database with the event catalog.
type Env struct {
	T      *testing.T
	DB     *db.DB
	Events *events.Catalog
	Store  *store.Store
	Log    *slog.Logger
	// Vault and Settings hold the module's section and a general section with
	// the time zone (UTC), like the platform's.
	Vault    *vault.Vault
	Settings *settings.Store
}

// New opens a database with the platform's and the module's tables.
func New(t *testing.T) *Env {
	t.Helper()
	d := dbtest.Open(t, Source)
	ev := events.NewCatalog()
	if err := ev.Declare(servers.Events...); err != nil {
		t.Fatal(err)
	}
	if err := ev.Declare(settings.ChangedEvent); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(bytes.Repeat([]byte{4}, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(d, v, ev)
	general := settings.Section{Name: "general", Module: "platform", Fields: []settings.Field{{Key: "general.time_zone", Kind: settings.String, Default: "UTC", MaxLen: 64}}}
	if err := st.Register(general, conf.Section); err != nil {
		t.Fatal(err)
	}
	return &Env{T: t, DB: d, Events: ev, Store: store.New(d, ev), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Vault: v, Settings: st}
}

// Recorded lists the events of a type, newest first.
func (e *Env) Recorded(typ string) []events.Event {
	e.T.Helper()
	l, err := events.List(context.Background(), e.DB.R, events.Filter{Type: typ, Limit: 100})
	if err != nil {
		e.T.Fatal(err)
	}
	return l
}

// AllRecorded counts every event in the database.
func (e *Env) AllRecorded() int {
	e.T.Helper()
	var n int
	if err := e.DB.R.GetContext(context.Background(), &n, `SELECT count(*) FROM events`); err != nil {
		e.T.Fatal(err)
	}
	return n
}

// AddServer inserts a server in a lifecycle state (an active one starts with
// health unknown) with a location and a template of its own if they do not
// exist yet, and returns its id.
func (e *Env) AddServer(name, state string) int64 {
	e.T.Helper()
	ctx := context.Background()
	mustExec := func(q string, args ...any) {
		e.T.Helper()
		if _, err := e.DB.W.ExecContext(ctx, q, args...); err != nil {
			e.T.Fatal(err)
		}
	}
	mustExec(`INSERT OR IGNORE INTO servers_locations (id, code, name, country, created_at) VALUES (1, 'nl', 'Netherlands', 'NL', '2026-10-01T00:00:00.000Z')`)
	mustExec(`INSERT OR IGNORE INTO servers_templates (id, slug, name, default_version, created_at) VALUES (1, 'tpl', 'Template', 1, '2026-10-01T00:00:00.000Z')`)
	var n int
	if err := e.DB.R.GetContext(ctx, &n, `SELECT count(*) FROM servers_servers`); err != nil {
		e.T.Fatal(err)
	}
	health := any(nil)
	if state == "active" {
		health = "unknown"
	}
	res, err := e.DB.W.ExecContext(ctx, `INSERT INTO servers_servers (location_id, number, name, ip, management_hostname, proxy_hostname, state, health,
			health_since, template_id, template_version, created_at, activated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, '2026-10-01T00:00:00.000Z', 1, 1, '2026-10-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
		n+1, name, "203.0.113."+itoa(n+1), name+".example.test", name+".example.test", state, health)
	if err != nil {
		e.T.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func itoa(n int) string { return strconv.Itoa(n) }

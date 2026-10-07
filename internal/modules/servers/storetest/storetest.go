// Package storetest gives the module's tests a migrated database with the
// module's event types declared. Only tests import it.
package storetest

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/migrations"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
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
}

// New opens a database with the platform's and the module's tables.
func New(t *testing.T) *Env {
	t.Helper()
	d := dbtest.Open(t, Source)
	ev := events.NewCatalog()
	if err := ev.Declare(servers.Events...); err != nil {
		t.Fatal(err)
	}
	return &Env{T: t, DB: d, Events: ev, Store: store.New(d, ev), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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

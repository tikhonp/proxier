// Package dbtest gives tests a real SQLite database in a temporary file, with
// the platform's migrations applied.
package dbtest

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/migrations"
)

type source struct {
	name string
	fs   fs.FS
}

func (s source) Name() string      { return s.name }
func (s source) Migrations() fs.FS { return s.fs }

// Platform is the platform's migration source.
var Platform db.MigrationSource = source{"platform", migrations.FS}

// Open returns a migrated database that is closed when the test ends. The
// platform's migrations always run first, then those of extra.
func Open(t testing.TB, extra ...db.MigrationSource) *db.DB {
	t.Helper()
	d := OpenEmpty(t)
	if err := db.MigrateUp(context.Background(), d, Discard, append([]db.MigrationSource{Platform}, extra...)...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

// OpenEmpty returns a database with no migrations applied.
func OpenEmpty(t testing.TB) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "proxier.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// Discard is a logger that drops everything.
var Discard = slog.New(slog.NewTextHandler(io.Discard, nil))

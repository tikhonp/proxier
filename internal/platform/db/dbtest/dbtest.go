// Package dbtest gives tests a real SQLite database in a temporary file, with
// the platform's migrations applied.
package dbtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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
	path := filepath.Join(t.TempDir(), "proxier.db")
	Seed(t, path, append([]db.MigrationSource{Platform}, extra...)...)
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
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

var (
	seedsMu sync.Mutex
	seeds   = map[string]*seed{}
)

type seed struct {
	once  sync.Once
	bytes []byte
	err   error
}

// Seed writes to path a database file with every migration of sources
// applied, as db.MigrateUp leaves it. Migrating under -race takes most of a
// second, so each set of migrations runs once per test binary and every test
// gets a copy of the file.
func Seed(t testing.TB, path string, sources ...db.MigrationSource) {
	t.Helper()
	key, err := seedKey(sources)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedsMu.Lock()
	s, ok := seeds[key]
	if !ok {
		s = &seed{}
		seeds[key] = s
	}
	seedsMu.Unlock()
	s.once.Do(func() { s.bytes, s.err = migrated(sources) })
	if s.err != nil {
		t.Fatalf("seed: %v", s.err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(path, s.bytes, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// seedKey names a set of migrations by its content, so two sources that
// share a name but not their files never share a seed.
func seedKey(sources []db.MigrationSource) (string, error) {
	h := sha256.New()
	for _, src := range sources {
		_, _ = io.WriteString(h, src.Name()+"\x00")
		fsys := src.Migrations()
		if fsys == nil {
			continue
		}
		err := fs.WalkDir(fsys, ".", func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(h, p+"\x00")
			_, _ = h.Write(b)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func migrated(sources []db.MigrationSource) ([]byte, error) {
	dir, err := os.MkdirTemp("", "proxier-seed-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "proxier.db")
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := db.MigrateUp(context.Background(), d, Discard, sources...); err != nil {
		_ = d.Close()
		return nil, err
	}
	// Everything goes into the main file, so the copy needs no -wal beside it.
	if _, err := d.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = d.Close()
		return nil, err
	}
	if err := d.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/pressly/goose/v3"
)

// MigrationSource is a module with its own migrations. module.Module
// satisfies it.
type MigrationSource interface {
	Name() string
	Migrations() fs.FS
}

// VersionTable is the goose version table of a module: every module keeps
// its own (docs/architecture.md#data).
func VersionTable(module string) string { return module + "_goose_db_version" }

// MigrationStatus is one migration of one module.
type MigrationStatus struct {
	Module  string
	Version int64
	Path    string
	Applied bool
}

func provider(d *DB, src MigrationSource) (*goose.Provider, error) {
	p, err := goose.NewProvider(goose.DialectSQLite3, d.W.DB, src.Migrations(),
		goose.WithTableName(VersionTable(src.Name())),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return nil, fmt.Errorf("migrations of %s: %w", src.Name(), err)
	}
	return p, nil
}

// MigrateUp applies every pending migration of every source, in order. Each
// migration runs in its own transaction, so a failing one leaves the schema
// as the previous migration left it, and the error stops the start.
func MigrateUp(ctx context.Context, d *DB, log *slog.Logger, sources ...MigrationSource) error {
	for _, src := range sources {
		if src.Migrations() == nil {
			continue
		}
		p, err := provider(d, src)
		if err != nil {
			return err
		}
		results, err := p.Up(ctx)
		for _, r := range results {
			log.Info("migration applied", "module", src.Name(), "version", r.Source.Version,
				"path", r.Source.Path, "duration_ms", r.Duration.Milliseconds())
		}
		if err != nil {
			var pe *goose.PartialError
			if errors.As(err, &pe) {
				return fmt.Errorf("migrate %s: %s failed: %w", src.Name(), pe.Failed.Source.Path, pe.Err)
			}
			return fmt.Errorf("migrate %s: %w", src.Name(), err)
		}
	}
	return nil
}

// MigrateDown rolls back the last applied migration of one source.
func MigrateDown(ctx context.Context, d *DB, log *slog.Logger, src MigrationSource) error {
	if src.Migrations() == nil {
		return fmt.Errorf("module %s has no migrations", src.Name())
	}
	p, err := provider(d, src)
	if err != nil {
		return err
	}
	r, err := p.Down(ctx)
	if err != nil {
		return fmt.Errorf("migrate down %s: %w", src.Name(), err)
	}
	log.Info("migration rolled back", "module", src.Name(), "version", r.Source.Version, "path", r.Source.Path)
	return nil
}

// Status lists every migration of every source and whether it is applied.
func Status(ctx context.Context, d *DB, sources ...MigrationSource) ([]MigrationStatus, error) {
	var out []MigrationStatus
	for _, src := range sources {
		if src.Migrations() == nil {
			continue
		}
		p, err := provider(d, src)
		if err != nil {
			return nil, err
		}
		st, err := p.Status(ctx)
		if err != nil {
			return nil, fmt.Errorf("status of %s: %w", src.Name(), err)
		}
		for _, s := range st {
			out = append(out, MigrationStatus{
				Module:  src.Name(),
				Version: s.Source.Version,
				Path:    s.Source.Path,
				Applied: s.State == goose.StateApplied,
			})
		}
	}
	return out, nil
}

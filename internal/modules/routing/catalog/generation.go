package catalog

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Batches: rows per write transaction, and rows per INSERT statement (SQLite
// caps the variables of one statement).
const (
	writeBatch = 5000
	stmtBatch  = 1000
)

// generation is a source's new catalog, before it is written.
type generation struct {
	source   string
	revision string
	entries  []store.CatalogEntry
	domains  []store.CatalogDomain
	includes []store.CatalogInclude
}

// write writes g as the source's next generation in short transactions, then
// puts it in force in one more, then deletes the old one. Readers join on the
// generation in force, so a search sees the old catalog until the flip. Rows
// of any other generation (a refresh cut short) are deleted first.
func (s *Service) write(ctx context.Context, g generation) error {
	cur, err := store.GetCatalogSource(ctx, s.d.DB.R, g.source)
	if err != nil {
		return err
	}
	if err := s.deleteStale(ctx, g.source, cur.Generation, false); err != nil {
		return err
	}
	gen := cur.Generation + 1
	for i := range g.entries {
		g.entries[i].Generation = gen
	}
	for i := range g.domains {
		g.domains[i].Generation = gen
	}
	for i := range g.includes {
		g.includes[i].Generation = gen
	}
	if err := batches(ctx, s, g.entries, store.InsertCatalogEntries); err != nil {
		return err
	}
	if err := batches(ctx, s, g.domains, store.InsertCatalogDomains); err != nil {
		return err
	}
	if err := batches(ctx, s, g.includes, store.InsertCatalogIncludes); err != nil {
		return err
	}
	if err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return store.CatalogFlip(ctx, tx, g.source, gen, g.revision, len(g.entries), db.At(s.d.Now()))
	}); err != nil {
		return err
	}
	return s.deleteStale(ctx, g.source, gen, false)
}

// batches writes rows writeBatch per transaction, stmtBatch per statement.
func batches[T any](ctx context.Context, s *Service, rows []T, insert func(context.Context, sqlx.ExtContext, []T) error) error {
	for i := 0; i < len(rows); i += writeBatch {
		part := rows[i:min(i+writeBatch, len(rows))]
		if err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			for j := 0; j < len(part); j += stmtBatch {
				if err := insert(ctx, tx, part[j:min(j+stmtBatch, len(part))]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// deleteStale deletes a source's rows of other generations than keep (only
// older ones with olderOnly), in batches.
func (s *Service) deleteStale(ctx context.Context, source string, keep int64, olderOnly bool) error {
	for {
		var n int64
		if err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			n, err = store.DeleteStaleCatalog(ctx, tx, source, keep, olderOnly, store.PruneBatch)
			return err
		}); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// Prune deletes the catalog rows of generations not in force: older ones
// always, newer ones (a refresh cut short) only while no catalog refresh is
// queued or running, since one may be writing them.
func (s *Service) Prune(ctx context.Context) error {
	busy, err := s.refreshing(ctx)
	if err != nil {
		return err
	}
	srcs, err := store.CatalogSources(ctx, s.d.DB.R)
	if err != nil {
		return err
	}
	for _, src := range srcs {
		if err := s.deleteStale(ctx, src.Source, src.Generation, busy); err != nil {
			return err
		}
	}
	return nil
}

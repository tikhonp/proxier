package shadowrocket

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Fetches reads a config's fetch log, newest first, with ids below before
// (0: from the newest).
func (s *Service) Fetches(ctx context.Context, id, before int64, limit int) ([]Fetch, error) {
	rows, err := store.ShadowrocketFetches(ctx, s.d.DB.R, id, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Fetch, 0, len(rows))
	for _, r := range rows {
		out = append(out, Fetch{ID: r.ID, At: r.At.Time, IP: r.IP, UserAgent: r.UserAgent})
	}
	return out, nil
}

// Prune deletes fetches older than the retention, in batches.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	cutoff := db.At(s.d.Now().Add(-store.ShadowrocketFetchRetention))
	var total int64
	for {
		var n int64
		if err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			n, err = store.PruneShadowrocketFetches(ctx, tx, cutoff, store.PruneBatch)
			return err
		}); err != nil {
			return total, err
		}
		if total += n; n == 0 {
			return total, nil
		}
	}
}

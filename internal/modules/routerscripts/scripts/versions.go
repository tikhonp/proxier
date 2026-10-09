package scripts

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
)

func toVersion(v store.Version, current int, gens map[int]int) Version {
	var warns []params.Finding
	_ = json.Unmarshal([]byte(v.Warnings), &warns)
	return Version{
		Number: v.Number, Body: v.Body, SHA256: v.SHA256, Warnings: warns, Notes: v.Notes,
		PublishedAt: v.PublishedAt.Time, PublishedBy: v.PublishedBy, Generations: gens[v.Number], Current: v.Number == current,
	}
}

// Versions reads a script's versions, newest first, without their bodies.
func (s *Service) Versions(ctx context.Context, id int64) ([]Version, error) {
	sc, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := store.Versions(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	gens, err := store.VersionGenerations(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(rows))
	for _, r := range rows {
		out = append(out, toVersion(r, sc.Current, gens))
	}
	return out, nil
}

// Version reads one version with its body; 0 is the current one.
func (s *Service) Version(ctx context.Context, id int64, n int) (Version, error) {
	sc, err := s.Get(ctx, id)
	if err != nil {
		return Version{}, err
	}
	if n == 0 {
		n = sc.Current
	}
	r, err := store.GetVersion(ctx, s.d.DB.R, id, n)
	if errors.Is(err, store.ErrNotFound) {
		return Version{}, ErrNoVersion
	}
	if err != nil {
		return Version{}, err
	}
	gens, err := store.VersionGenerations(ctx, s.d.DB.R, id)
	if err != nil {
		return Version{}, err
	}
	return toVersion(r, sc.Current, gens), nil
}

// MakeCurrent points new generations at version n; the current one again
// changes nothing.
func (s *Service) MakeCurrent(ctx context.Context, id int64, n int, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		sc, err := script(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := store.GetVersion(ctx, tx, id, n); errors.Is(err, store.ErrNotFound) {
			return ErrNoVersion
		} else if err != nil {
			return err
		}
		if sc.Current == n {
			return nil
		}
		if err := store.SetCurrent(ctx, tx, id, n); err != nil {
			return err
		}
		return s.record(ctx, tx, "routerscript.current_changed", id, actor, map[string]any{"from": sc.Current, "to": n})
	})
}

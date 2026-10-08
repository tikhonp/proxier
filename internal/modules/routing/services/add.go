package services

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// Add adds an upstream service: the tag is computed without the network; a
// taken tag is a *TagTakenError and nothing is resolved; otherwise the
// selector is resolved (outside any transaction) and its first snapshot is
// accepted with the service.
func (s *Service) Add(ctx context.Context, raw string, actor string) (int64, error) {
	sel, err := selector.Parse(raw)
	if err != nil {
		return 0, err
	}
	tag, err := sel.Tag()
	if err != nil {
		return 0, err
	}
	if err := s.tagFree(ctx, s.d.DB.R, tag); err != nil {
		return 0, err
	}
	res, stored, err := s.resolve(ctx, sel)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := s.tagFree(ctx, tx, tag); err != nil {
			return err
		}
		var err error
		id, err = store.InsertService(ctx, tx, store.Service{Tag: tag, Source: string(sel.Source), Selector: stored, CreatedAt: nowAt(s)})
		if err != nil {
			return err
		}
		snap, err := s.newSnapshot(id, stored, res.Portal, res.Kind, res.Set, nil)
		if err != nil {
			return err
		}
		if _, err := store.InsertSnapshot(ctx, tx, snap); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.service_added", id, actor, map[string]any{
			"selector": stored, "tag": tag, "source": string(sel.Source), "origin": "",
		})
	})
	return id, err
}

// tagFree is a *TagTakenError when a service has the tag.
func (s *Service) tagFree(ctx context.Context, q sqlx.QueryerContext, tag string) error {
	ex, err := store.ServiceByTag(ctx, q, tag)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return &TagTakenError{Tag: tag, Existing: item(ex)}
}

// resolve fetches a selector within the interactive deadline and returns the
// spelling to store: an unpinned iplist selector found off main is pinned.
func (s *Service) resolve(ctx context.Context, sel selector.Selector) (sources.Resolved, string, error) {
	ctx, cancel := context.WithTimeout(ctx, Interactive)
	defer cancel()
	res, err := s.d.Resolver.Resolve(ctx, sel)
	if err != nil {
		return res, "", err
	}
	stored := sel.Pin(res.Portal).String()
	if res.Set.Count() == 0 {
		return res, "", &EmptyResolveError{Selector: stored, Skipped: len(res.Set.Skipped)}
	}
	return res, stored, nil
}

// Switch gives an upstream service another selector with the same tag. Its
// snapshot is accepted at once, without the safety checks: the admin chose
// the source.
func (s *Service) Switch(ctx context.Context, id int64, raw string, actor string) error {
	it, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if it.Source == selector.Custom {
		return ErrCustom
	}
	sel, err := selector.Parse(raw)
	if err != nil {
		return err
	}
	tag, err := sel.Tag()
	if err != nil {
		return err
	}
	if tag != it.Tag {
		return &SwitchTagError{Want: it.Tag, Got: tag}
	}
	if sel.String() == it.Selector {
		return nil
	}
	res, stored, err := s.resolve(ctx, sel)
	if err != nil {
		return err
	}
	if stored == it.Selector {
		return nil
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetService(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := store.SetSource(ctx, tx, id, string(sel.Source), stored); err != nil {
			return err
		}
		old, hash, err := acceptedSet(ctx, tx, id)
		if err != nil {
			return err
		}
		if res.Set.Hash() != hash {
			snap, err := s.newSnapshot(id, stored, res.Portal, res.Kind, res.Set, &old)
			if err != nil {
				return err
			}
			if _, err := store.Accept(ctx, tx, snap); err != nil {
				return err
			}
			if err := s.d.Marker.Mark(ctx, tx, change.Change{Services: []int64{id}, Actor: actor, Why: tag + " switched source"}); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, "routing.service_updated", id, actor, map[string]any{
			"changes": "source", "from": cur.Selector, "to": stored,
		})
	})
}

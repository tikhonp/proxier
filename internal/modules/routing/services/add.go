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

// Prepared is a parsed and resolved new upstream service, not yet written.
type Prepared struct {
	Selector selector.Selector // as it will be stored (pinned when found off main)
	Tag      string
	Resolved sources.Resolved
}

// Stored is the selector's stored spelling.
func (p Prepared) Stored() string { return p.Selector.String() }

// Prepare parses a selector, checks its tag is free (a *TagTakenError
// resolves nothing) and resolves it, outside any transaction. Nothing is
// written.
func (s *Service) Prepare(ctx context.Context, raw string) (Prepared, error) {
	sel, err := selector.Parse(raw)
	if err != nil {
		return Prepared{}, err
	}
	tag, err := sel.Tag()
	if err != nil {
		return Prepared{}, err
	}
	if err := s.tagFree(ctx, s.d.DB.R, tag); err != nil {
		return Prepared{}, err
	}
	res, stored, err := s.resolve(ctx, sel)
	if err != nil {
		return Prepared{}, err
	}
	pinned, err := selector.Parse(stored)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Selector: pinned, Tag: tag, Resolved: res}, nil
}

// CreateTx writes a prepared service with its first, accepted snapshot and
// records service_added, in the caller's transaction. The tag is checked
// again: another add may have taken it meanwhile.
func (s *Service) CreateTx(ctx context.Context, tx *sqlx.Tx, p Prepared, actor string) (int64, error) {
	if err := s.tagFree(ctx, tx, p.Tag); err != nil {
		return 0, err
	}
	stored := p.Stored()
	id, err := store.InsertService(ctx, tx, store.Service{Tag: p.Tag, Source: string(p.Selector.Source), Selector: stored, CreatedAt: nowAt(s)})
	if err != nil {
		return 0, err
	}
	snap, err := s.newSnapshot(id, stored, p.Resolved.Portal, p.Resolved.Kind, p.Resolved.Set, nil)
	if err != nil {
		return 0, err
	}
	if _, err := store.InsertSnapshot(ctx, tx, snap); err != nil {
		return 0, err
	}
	return id, s.record(ctx, tx, "routing.service_added", id, actor, map[string]any{
		"selector": stored, "tag": p.Tag, "source": string(p.Selector.Source), "origin": "",
	})
}

// Add adds an upstream service: the tag is computed without the network; a
// taken tag is a *TagTakenError and nothing is resolved; otherwise the
// selector is resolved (outside any transaction) and its first snapshot is
// accepted with the service. It is Prepare, then CreateTx in its own Write.
func (s *Service) Add(ctx context.Context, raw string, actor string) (int64, error) {
	p, err := s.Prepare(ctx, raw)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, err = s.CreateTx(ctx, tx, p, actor)
		return err
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

// Preview parses and resolves a selector as Add would, without the tag check
// and within the interactive deadline: the search page's preview. Nothing is
// written. A selector with no usable name returns what it resolved to with
// its *EmptyResolveError, so its skipped entries can be shown.
func (s *Service) Preview(ctx context.Context, raw string) (Prepared, error) {
	sel, err := selector.Parse(raw)
	if err != nil {
		return Prepared{}, err
	}
	tag, err := sel.Tag()
	if err != nil {
		return Prepared{}, err
	}
	res, stored, err := s.resolve(ctx, sel)
	p := Prepared{Selector: sel, Tag: tag, Resolved: res}
	if err != nil {
		return p, err
	}
	if p.Selector, err = selector.Parse(stored); err != nil {
		return Prepared{}, err
	}
	return p, nil
}

// BySelector reads the service stored with exactly this selector.
func (s *Service) BySelector(ctx context.Context, sel string) (Item, error) {
	r, err := store.ServiceBySelector(ctx, s.d.DB.R, sel)
	if errors.Is(err, store.ErrNotFound) {
		return Item{}, ErrNotFound
	}
	return item(r), err
}

package lists

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Add appends services to a list (in tag order, skipping those already in
// it). A *GuardError writes nothing and records the refusal.
func (s *Service) Add(ctx context.Context, id int64, serviceIDs []int64, actor string) error {
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error { return s.AddTx(ctx, tx, id, serviceIDs, actor) })
	s.Refused(ctx, err, actor)
	return err
}

// AddTx is Add in the caller's transaction (Add service with lists, the
// import). The caller records a refusal after its transaction rolled back.
func (s *Service) AddTx(ctx context.Context, tx *sqlx.Tx, id int64, serviceIDs []int64, actor string) error {
	l, err := getList(ctx, tx, id)
	if err != nil {
		return err
	}
	cur, err := store.Members(ctx, tx, id)
	if err != nil {
		return err
	}
	in := map[int64]bool{}
	for _, m := range cur {
		in[m.ServiceID] = true
	}
	var add []store.Service
	for _, sid := range serviceIDs {
		if in[sid] {
			continue
		}
		in[sid] = true
		svc, err := store.GetService(ctx, tx, sid)
		if errors.Is(err, store.ErrNotFound) {
			return services.ErrNotFound
		}
		if err != nil {
			return err
		}
		add = append(add, svc)
	}
	if len(add) == 0 {
		return nil
	}
	sort.Slice(add, func(i, j int) bool { return add[i].Tag < add[j].Tag })
	srv, err := s.servers(ctx)
	if err != nil {
		return err
	}
	for _, svc := range add {
		set, err := accepted(ctx, tx, svc.ID)
		if err != nil {
			return err
		}
		if ge := guard(set.Suffix, set.Exact, srv); ge != nil {
			ge.List, ge.Service, ge.ListIDs = l.Name, svc.Tag, []int64{id}
			return ge
		}
	}
	at := db.At(s.d.Now())
	tags := make([]string, 0, len(add))
	for i, svc := range add {
		if err := store.AddMember(ctx, tx, id, svc.ID, len(cur)+i+1, at); err != nil {
			return err
		}
		tags = append(tags, svc.Tag)
	}
	joined := strings.Join(tags, ", ")
	if err := s.d.Marker.Mark(ctx, tx, change.Change{Lists: []int64{id}, Actor: actor, Why: l.Name + ": added " + joined}); err != nil {
		return err
	}
	return s.record(ctx, tx, "routing.list_updated", Subject(id), actor, map[string]any{"changes": "services", "added": joined})
}

// Remove takes a service out of a list; the rest close up.
func (s *Service) Remove(ctx context.Context, id, serviceID int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		l, err := getList(ctx, tx, id)
		if err != nil {
			return err
		}
		svc, err := store.GetService(ctx, tx, serviceID)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotMember
		}
		if err != nil {
			return err
		}
		removed, err := store.RemoveMember(ctx, tx, id, serviceID)
		if err != nil {
			return err
		}
		if !removed {
			return ErrNotMember
		}
		rest, err := store.Members(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := store.SetPositions(ctx, tx, id, ids(rest)); err != nil {
			return err
		}
		if err := s.d.Marker.Mark(ctx, tx, change.Change{Lists: []int64{id}, Actor: actor, Why: l.Name + ": removed " + svc.Tag}); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.list_updated", Subject(id), actor, map[string]any{"changes": "services", "removed": svc.Tag})
	})
}

func ids(ms []store.Member) []int64 {
	out := make([]int64, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ServiceID)
	}
	return out
}

// Move swaps a service with its neighbour below (down) or above; at an end
// nothing changes and nothing is recorded. It returns the ownership before
// and after, for the band.
func (s *Service) Move(ctx context.Context, id, serviceID int64, down bool, actor string) (before, after own.Result, err error) {
	return s.reorder(ctx, id, actor, func(cur []int64) ([]int64, error) {
		i := slices.Index(cur, serviceID)
		if i < 0 {
			return nil, ErrStaleOrder
		}
		j := i - 1
		if down {
			j = i + 1
		}
		next := slices.Clone(cur)
		if j >= 0 && j < len(next) {
			next[i], next[j] = next[j], next[i]
		}
		return next, nil
	})
}

// SetOrder writes the whole order: a permutation of the members, else
// ErrStaleOrder. With expect (Undo), the current order must be exactly that.
func (s *Service) SetOrder(ctx context.Context, id int64, serviceIDs, expect []int64, actor string) (own.Result, own.Result, error) {
	return s.reorder(ctx, id, actor, func(cur []int64) ([]int64, error) {
		if expect != nil && !slices.Equal(cur, expect) {
			return nil, ErrStaleOrder
		}
		a, b := slices.Clone(cur), slices.Clone(serviceIDs)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			return nil, ErrStaleOrder
		}
		return serviceIDs, nil
	})
}

func (s *Service) reorder(ctx context.Context, id int64, actor string, next func(cur []int64) ([]int64, error)) (before, after own.Result, err error) {
	srv, err := s.servers(ctx)
	if err != nil {
		return before, after, err
	}
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		l, err := getList(ctx, tx, id)
		if err != nil {
			return err
		}
		ms, err := members(ctx, tx, id)
		if err != nil {
			return err
		}
		cur := make([]int64, 0, len(ms))
		byID := map[int64]own.Member{}
		for _, m := range ms {
			cur = append(cur, m.ServiceID)
			byID[m.ServiceID] = m
		}
		order, err := next(cur)
		if err != nil {
			return err
		}
		before = own.Compute(ms, srv, nil)
		if slices.Equal(order, cur) {
			after = before
			return nil
		}
		moved := make([]own.Member, 0, len(order))
		for _, sid := range order {
			moved = append(moved, byID[sid])
		}
		after = own.Compute(moved, srv, nil)
		if err := store.SetPositions(ctx, tx, id, order); err != nil {
			return err
		}
		if err := s.d.Marker.Mark(ctx, tx, change.Change{Lists: []int64{id}, Actor: actor, Why: l.Name + " reordered"}); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.list_updated", Subject(id), actor, map[string]any{"changes": "order", "reordered": true})
	})
	return before, after, err
}

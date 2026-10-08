package subs

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
)

// Addable lists the servers the catalog serves that aren't members, by name.
func (s *Service) Addable(ctx context.Context, id int64) ([]servers.ServerEndpoints, error) {
	members, err := s.Members(ctx, id)
	if err != nil || s.d.Catalog == nil {
		return nil, err
	}
	list, err := s.d.Catalog.Active(ctx)
	if err != nil {
		return nil, err
	}
	var out []servers.ServerEndpoints
	for _, se := range list {
		if !slices.ContainsFunc(members, func(m Member) bool { return m.ServerID == se.ServerID }) {
			out = append(out, se)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// AddServers appends the servers at the end, by name. A server the catalog
// doesn't serve refuses the whole save (ErrNotServed); members already in it
// are left where they are.
func (s *Service) AddServers(ctx context.Context, id int64, serverIDs []int64, actor string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if s.d.Catalog == nil {
		return ErrNotServed
	}
	// Ask the catalog before the transaction: nothing remote or slow inside it.
	var add []servers.ServerEndpoints
	for _, sid := range serverIDs {
		if slices.ContainsFunc(add, func(se servers.ServerEndpoints) bool { return se.ServerID == sid }) {
			continue
		}
		se, ok, err := s.d.Catalog.Server(ctx, sid)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotServed
		}
		add = append(add, se)
	}
	sort.Slice(add, func(i, j int) bool { return add[i].Name < add[j].Name })
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := s.append(ctx, tx, id, add, actor, map[string]any{})
		return err
	})
}

// append adds the servers that aren't members yet at the end and records one
// servers_changed with extra in its payload; false when nothing was added.
func (s *Service) append(ctx context.Context, tx *sqlx.Tx, id int64, add []servers.ServerEndpoints, actor string, extra map[string]any) (bool, error) {
	members, err := store.Members(ctx, tx, id)
	if err != nil {
		return false, err
	}
	var names []string
	for _, se := range add {
		if slices.ContainsFunc(members, func(m store.Member) bool { return m.ServerID == se.ServerID }) {
			continue
		}
		members = append(members, store.Member{ServerID: se.ServerID})
		if err := store.AddMember(ctx, tx, id, se.ServerID, se.Name, len(members), s.now()); err != nil {
			return false, err
		}
		names = append(names, se.Name)
	}
	if len(names) == 0 {
		return false, nil
	}
	p := map[string]any{"added": strings.Join(names, ", "), "removed": "", "reordered": false}
	for k, v := range extra {
		p[k] = v
	}
	return true, s.record(ctx, tx, "subscription.servers_changed", id, actor, p)
}

// RemoveServer takes a server out of the subscription; the others close up.
func (s *Service) RemoveServer(ctx context.Context, id, serverID int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.GetSubscription(ctx, tx, id); errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		removed, err := s.remove(ctx, tx, id, serverID, actor, nil)
		if err == nil && !removed {
			return ErrNotFound
		}
		return err
	})
}

func (s *Service) remove(ctx context.Context, tx *sqlx.Tx, id, serverID int64, actor string, extra map[string]any) (bool, error) {
	members, err := store.Members(ctx, tx, id)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(members, func(m store.Member) bool { return m.ServerID == serverID })
	if i < 0 {
		return false, nil
	}
	name := members[i].ServerName
	if _, err := store.RemoveMember(ctx, tx, id, serverID); err != nil {
		return false, err
	}
	members = slices.Delete(members, i, i+1)
	if err := store.SetPositions(ctx, tx, id, ids(members)); err != nil {
		return false, err
	}
	p := map[string]any{"added": "", "removed": name, "reordered": false}
	for k, v := range extra {
		p[k] = v
	}
	return true, s.record(ctx, tx, "subscription.servers_changed", id, actor, p)
}

func ids(members []store.Member) []int64 {
	out := make([]int64, len(members))
	for i, m := range members {
		out[i] = m.ServerID
	}
	return out
}

// Move swaps a server with its neighbour below (down) or above; at an end
// nothing changes and nothing is recorded.
func (s *Service) Move(ctx context.Context, id, serverID int64, down bool, actor string) error {
	return s.reorder(ctx, id, actor, func(cur []int64) ([]int64, error) {
		i := slices.Index(cur, serverID)
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

// SetOrder writes the whole order; it must be a permutation of the members,
// else ErrStaleOrder.
func (s *Service) SetOrder(ctx context.Context, id int64, serverIDs []int64, actor string) error {
	return s.reorder(ctx, id, actor, func(cur []int64) ([]int64, error) {
		a, b := slices.Clone(cur), slices.Clone(serverIDs)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			return nil, ErrStaleOrder
		}
		return serverIDs, nil
	})
}

func (s *Service) reorder(ctx context.Context, id int64, actor string, next func(cur []int64) ([]int64, error)) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.GetSubscription(ctx, tx, id); errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		members, err := store.Members(ctx, tx, id)
		if err != nil {
			return err
		}
		cur := ids(members)
		order, err := next(cur)
		if err != nil || slices.Equal(order, cur) {
			return err
		}
		if err := store.SetPositions(ctx, tx, id, order); err != nil {
			return err
		}
		return s.record(ctx, tx, "subscription.servers_changed", id, actor, map[string]any{"added": "", "removed": "", "reordered": true})
	})
}

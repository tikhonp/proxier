package routers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// ErrNoSuchTag is an unmanaged tag the router doesn't have (any more).
var ErrNoSuchTag = errors.New("routers: no such unmanaged tag")

// UnmanagedTag is a tag on a router that Proxier never installed (the plan
// constant Unmanaged is the plan's word for it).
type UnmanagedTag struct {
	Tag       string
	Entries   int
	Names     []routeros.Entry
	FirstSeen time.Time
	LastSeen  time.Time
	Ignored   bool
	Catalog   []string       // selectors the catalog offers for it, spelled
	Existing  *services.Item // a service with that tag
}

// UnmanagedTags are the tags of a read that are neither applied nor desired
// (infra pins are never tags): the read's unmanaged tags, sorted.
func UnmanagedTags(st State, applied map[string]Applied, desired map[string]bool) []string {
	var out []string
	for tag, t := range st.Tags {
		if t.empty() || desired[tag] || strings.HasPrefix(tag, routeros.InfraPrefix) {
			continue
		}
		if _, ok := applied[tag]; ok {
			continue
		}
		out = append(out, tag)
	}
	slices.Sort(out)
	return out
}

// recordUnmanaged writes what a read found that Proxier never installed, in
// the read's transaction: a row per unmanaged tag (gone ones deleted), and
// routing.unmanaged_tags_found when the tags not ignored include one not
// notified yet. The notified set follows the current one, so a tag that
// leaves and comes back notifies again.
func (s *Service) recordUnmanaged(ctx context.Context, tx *sqlx.Tx, rt store.Router, st State, tags []string, actor string) error {
	cur, err := store.UnmanagedOf(ctx, tx, rt.ID)
	if err != nil {
		return err
	}
	prev := map[string]store.Unmanaged{}
	for _, u := range cur {
		prev[u.Tag] = u
	}
	at := db.At(st.ReadAt)
	var shown []string
	for _, tag := range tags {
		t := st.Tags[tag]
		names := make([]routeros.Entry, 0, min(len(t.DNS), store.MaxUnmanagedNames))
		for _, d := range t.DNS {
			if len(names) == store.MaxUnmanagedNames {
				break
			}
			names = append(names, routeros.Entry{Name: d.Name, Exact: !d.Subdomain})
		}
		routeros.Sort(names)
		first, ignored := at, false
		if p, ok := prev[tag]; ok {
			first, ignored = p.FirstSeen, !p.IgnoredAt.IsZero()
			delete(prev, tag)
		}
		if !ignored {
			shown = append(shown, tag)
		}
		b, err := json.Marshal(names)
		if err != nil {
			return err
		}
		if err := store.SeeUnmanaged(ctx, tx, store.Unmanaged{
			RouterID: rt.ID, Tag: tag, Entries: len(t.DNS), Names: string(b), FirstSeen: first, LastSeen: at,
		}); err != nil {
			return err
		}
	}
	for tag := range prev {
		if err := store.DeleteUnmanaged(ctx, tx, rt.ID, tag); err != nil {
			return err
		}
	}
	notified := splitTags(rt.UnmanagedNotified)
	var fresh []string
	for _, tag := range shown {
		if !slices.Contains(notified, tag) {
			fresh = append(fresh, tag)
		}
	}
	if now := strings.Join(shown, ","); now != rt.UnmanagedNotified {
		if err := store.SetUnmanagedNotified(ctx, tx, rt.ID, now); err != nil {
			return err
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	return s.record(ctx, tx, "routing.unmanaged_tags_found", rt.ID, actor, map[string]any{"tags": strings.Join(fresh, ", ")})
}

// Unmanaged reads a router's unmanaged tags with what adopting each could
// use: the catalog's selectors of that tag and an existing service.
func (s *Service) Unmanaged(ctx context.Context, id int64) ([]UnmanagedTag, error) {
	rows, err := store.UnmanagedOf(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make([]UnmanagedTag, 0, len(rows))
	for _, r := range rows {
		u, err := s.unmanagedOf(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// UnmanagedOne reads one unmanaged tag; ErrNoSuchTag when the router hasn't it.
func (s *Service) UnmanagedOne(ctx context.Context, id int64, tag string) (UnmanagedTag, error) {
	r, err := store.GetUnmanaged(ctx, s.d.DB.R, id, tag)
	if errors.Is(err, store.ErrNotFound) {
		return UnmanagedTag{}, ErrNoSuchTag
	} else if err != nil {
		return UnmanagedTag{}, err
	}
	return s.unmanagedOf(ctx, r)
}

func (s *Service) unmanagedOf(ctx context.Context, r store.Unmanaged) (UnmanagedTag, error) {
	u := UnmanagedTag{Tag: r.Tag, Entries: r.Entries, FirstSeen: r.FirstSeen.Time, LastSeen: r.LastSeen.Time, Ignored: !r.IgnoredAt.IsZero()}
	if err := json.Unmarshal([]byte(r.Names), &u.Names); err != nil {
		return u, err
	}
	if s.d.Catalog != nil {
		offers, err := s.d.Catalog.Offers(ctx, r.Tag)
		if err != nil {
			return u, err
		}
		u.Catalog = offers
	}
	if it, err := s.d.Services.ByTag(ctx, r.Tag); err == nil {
		u.Existing = &it
	} else if !errors.Is(err, services.ErrNotFound) {
		return u, err
	}
	return u, nil
}

// RemoveUnmanaged removes an unmanaged tag from the router: a sync now that
// plans that tag as a removal while it is still unmanaged.
func (s *Service) RemoveUnmanaged(ctx context.Context, id int64, tag, actor string) (int64, error) {
	if _, err := store.GetUnmanaged(ctx, s.d.DB.R, id, tag); errors.Is(err, store.ErrNotFound) {
		return 0, ErrNoSuchTag
	} else if err != nil {
		return 0, err
	}
	return s.syncAtOnce(ctx, id, syncPayload{Trigger: TriggerUnmanaged, Remove: []string{tag}}, actor)
}

// Ignore hides an unmanaged tag from reports and notifications (it stays on
// the router), or brings it back. A change that changes nothing records nothing.
func (s *Service) Ignore(ctx context.Context, id int64, tag string, ignore bool, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		u, err := store.GetUnmanaged(ctx, tx, id, tag)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNoSuchTag
		} else if err != nil {
			return err
		}
		if u.IgnoredAt.IsZero() != ignore {
			return nil
		}
		var at db.Time
		if ignore {
			at = s.now()
		} else {
			// back in the reports without a notification: the admin knows it
			rt, err := store.GetRouter(ctx, tx, id)
			if err != nil {
				return err
			}
			if n := splitTags(rt.UnmanagedNotified); !slices.Contains(n, tag) {
				n = append(n, tag)
				slices.Sort(n)
				if err := store.SetUnmanagedNotified(ctx, tx, id, strings.Join(n, ",")); err != nil {
					return err
				}
			}
		}
		if err := store.SetUnmanagedIgnored(ctx, tx, id, tag, at); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.unmanaged_tag_ignored", id, actor, map[string]any{"tag": tag, "ignored": ignore})
	})
}

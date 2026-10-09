package routers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Sync triggers.
const (
	TriggerChange    = "change"
	TriggerManual    = "manual"
	TriggerInitial   = "initial"
	TriggerDrift     = "drift"
	TriggerResume    = "resume"
	TriggerUnmanaged = "unmanaged"
)

// maxWhy is how many reasons a coalesced sync keeps.
const maxWhy = 10

// syncPayload is routing.sync's payload.
type syncPayload struct {
	RouterID int64    `json:"router_id"`
	Trigger  string   `json:"trigger"`
	Manual   bool     `json:"manual,omitempty"`
	Why      []string `json:"why,omitempty"`
	Remove   []string `json:"remove,omitempty"` // unmanaged tags to remove (3f)
}

// mergeSync keeps manual when either request had it and unions why and remove.
func mergeSync(queued, incoming json.RawMessage) (json.RawMessage, error) {
	var a, b syncPayload
	if err := json.Unmarshal(queued, &a); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(incoming, &b); err != nil {
		return nil, err
	}
	switch {
	case b.Manual:
		a.Manual, a.Trigger = true, TriggerManual
	case a.Trigger == TriggerChange && b.Trigger != "":
		a.Trigger = b.Trigger // a sync asked for at once says why it runs
	}
	for _, w := range b.Why {
		if !slices.Contains(a.Why, w) && len(a.Why) < maxWhy {
			a.Why = append(a.Why, w)
		}
	}
	for _, t := range b.Remove {
		if !slices.Contains(a.Remove, t) {
			a.Remove = append(a.Remove, t)
		}
	}
	return json.Marshal(a)
}

func markChange(routers []int64, actor, why string) change.Change {
	return change.Change{Routers: routers, Actor: actor, Why: why}
}

// enqueueSync asks for a sync of the router inside tx, coalesced per router.
func (s *Service) enqueueSync(ctx context.Context, tx *sqlx.Tx, id int64, p syncPayload, delay time.Duration, actor string) (jobs.Enqueued, error) {
	p.RouterID = id
	if actor == "" {
		actor = events.ActorSystem
	}
	return s.d.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobSync, Payload: p, ResourceKey: resourceKey(id), CoalescingKey: resourceKey(id), Delay: delay,
		Subject: Subject(id), CreatedBy: actor,
	})
}

// Marker turns changes into syncs: every active router that has connected
// at least once and follows an affected list (or is named) gets routing.sync
// routing.sync_delay later, in the caller's transaction. The jobs system
// coalesces them, pushing a queued sync's start back to the delay after the
// last change.
func (s *Service) Marker() change.Marker { return marker{s} }

type marker struct{ s *Service }

func (m marker) Mark(ctx context.Context, tx *sqlx.Tx, c change.Change) error {
	ids, err := store.MarkedRouters(ctx, tx, c.Lists, c.Services, c.Routers)
	if err != nil || len(ids) == 0 {
		return err
	}
	delay, err := m.s.d.Settings.GetDuration(ctx, conf.SyncDelay)
	if err != nil {
		return err
	}
	p := syncPayload{Trigger: TriggerChange}
	if c.Why != "" {
		p.Why = []string{c.Why}
	}
	for _, id := range ids {
		if _, err := m.s.enqueueSync(ctx, tx, id, p, delay, c.Actor); err != nil {
			return err
		}
	}
	return nil
}

// SyncNow queues a sync at once; one merged into a delayed sync starts it now.
func (s *Service) SyncNow(ctx context.Context, id int64, actor string) (int64, error) {
	return s.syncAtOnce(ctx, id, syncPayload{Trigger: TriggerManual, Manual: true}, actor)
}

// syncAtOnce queues a sync of an active router with no delay; merged into a
// delayed one, it starts that one now.
func (s *Service) syncAtOnce(ctx context.Context, id int64, p syncPayload, actor string) (int64, error) {
	var e jobs.Enqueued
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		r, err := store.GetRouter(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if r.State != StateActive {
			return ErrNotActive
		}
		e, err = s.enqueueSync(ctx, tx, id, p, 0, actor)
		return err
	})
	if err != nil {
		return 0, err
	}
	if e.Merged {
		if err := s.d.Jobs.RunNow(ctx, e.ID); err != nil && !errors.Is(err, jobs.ErrNotActive) {
			return 0, err
		}
	}
	s.d.Jobs.Kick()
	return e.ID, nil
}

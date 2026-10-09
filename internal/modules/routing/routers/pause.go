package routers

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// ErrState is an action the router's state doesn't allow.
var ErrState = errors.New("routers: not in this state")

// cancelQueued cancels the router's queued jobs of these types (a running one
// finishes, and its next step finds the router's new state).
func (s *Service) cancelQueued(ctx context.Context, tx *sqlx.Tx, id int64, actor string, types ...string) error {
	for _, t := range types {
		if _, err := s.d.Jobs.CancelQueued(ctx, tx, t, Subject(id), actor); err != nil {
			return err
		}
	}
	return nil
}

// Pause stops an active router's syncs, previews and drift checks: their
// queued jobs are cancelled and marks skip it until Resume.
func (s *Service) Pause(ctx context.Context, id int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		rt, err := store.GetRouter(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		switch rt.State {
		case StatePaused:
			return nil
		case StateActive:
		default:
			return ErrState
		}
		if err := store.SetRouterState(ctx, tx, id, StatePaused); err != nil {
			return err
		}
		if err := s.cancelQueued(ctx, tx, id, actor, JobSync, JobPreview, JobDrift); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.router_paused", id, actor, nil)
	})
}

// Resume makes a paused router active again and queues a full sync now (a
// router that never connected still waits for its first passing test).
func (s *Service) Resume(ctx context.Context, id int64, actor string) error {
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		rt, err := store.GetRouter(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		switch rt.State {
		case StateActive:
			return nil
		case StatePaused:
		default:
			return ErrState
		}
		if err := store.SetRouterState(ctx, tx, id, StateActive); err != nil {
			return err
		}
		if err := s.record(ctx, tx, "routing.router_resumed", id, actor, nil); err != nil {
			return err
		}
		if rt.ConnectedAt.IsZero() {
			return nil
		}
		_, err = s.enqueueSync(ctx, tx, id, syncPayload{Trigger: TriggerResume}, 0, actor)
		return err
	})
	if err == nil {
		s.d.Jobs.Kick()
	}
	return err
}

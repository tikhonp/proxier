package routers

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Remove removes a router from Proxier. Kept (clean false): in one Write its
// queued jobs are cancelled and the row is deleted with everything recorded
// about it; the router is untouched. Cleaned: the router is removing and
// routing.remove removes every tag Proxier installed, then deletes the row;
// its job id is returned. Pinned host keys stay (Settings → SSH forgets them).
func (s *Service) Remove(ctx context.Context, id int64, clean bool, actor string) (int64, error) {
	var jobID int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		rt, err := store.GetRouter(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := s.cancelQueued(ctx, tx, id, actor, JobSync, JobPreview, JobDrift, JobProbe, JobTest, JobRemove); err != nil {
			return err
		}
		if !clean {
			if err := s.record(ctx, tx, "routing.router_removed", id, actor, map[string]any{"name": rt.Name, "cleaned": false}); err != nil {
				return err
			}
			return store.DeleteRouter(ctx, tx, id)
		}
		if rt.State == StateRemoving {
			return ErrState
		}
		if err := store.SetRouterState(ctx, tx, id, StateRemoving); err != nil {
			return err
		}
		e, err := s.d.Jobs.Enqueue(ctx, tx, jobs.Request{
			Type: JobRemove, Payload: syncPayload{RouterID: id, Manual: true}, ResourceKey: resourceKey(id),
			Subject: Subject(id), CreatedBy: actor,
		})
		jobID = e.ID
		return err
	})
	if err == nil && clean {
		s.d.Jobs.Kick()
	}
	return jobID, err
}

// stepDelete ends a cleaned removal: the row goes, router_removed{cleaned}.
func (s *Service) stepDelete(ctx context.Context, r *jobs.Run) error {
	var p syncPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		rt, err := store.GetRouter(ctx, tx, p.RouterID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if rt.State != StateRemoving {
			return nil
		}
		if err := s.record(ctx, tx, "routing.router_removed", rt.ID, r.Info().Actor(), map[string]any{"name": rt.Name, "cleaned": true}); err != nil {
			return err
		}
		return store.DeleteRouter(ctx, tx, rt.ID)
	})
}

// RemovalJob is a removing router's newest removal job (Retry needs it); 0
// when it has none.
func (s *Service) RemovalJob(ctx context.Context, id int64) (int64, error) {
	r, err := store.LatestSync(ctx, s.d.DB.R, id, "removal")
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	return r.JobID, err
}

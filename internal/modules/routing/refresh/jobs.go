package refresh

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Job and schedule names.
const (
	JobRefresh = "routing.refresh"
	JobRound   = "routing.refresh_round"
)

// RoundSubject is the daily round's subject ("Upstream refresh").
var RoundSubject = events.Subject{Type: "routing", ID: "refresh"}

type refreshPayload struct {
	ServiceID int64 `json:"service_id"`
}

func resourceKey(id int64) string { return "service:" + strconv.FormatInt(id, 10) }

func refreshRequest(id int64, actor string) jobs.Request {
	return jobs.Request{
		Type: JobRefresh, Payload: refreshPayload{ServiceID: id}, ResourceKey: resourceKey(id), CoalescingKey: resourceKey(id),
		CreatedBy: actor,
	}
}

// RefreshNow queues a refresh of one upstream service and returns its job
// (an already queued one when the request coalesced into it).
func (s *Service) RefreshNow(ctx context.Context, serviceID int64, actor string) (int64, error) {
	it, err := s.d.Services.Get(ctx, serviceID)
	if err != nil {
		return 0, err
	}
	if it.Source == selector.Custom {
		return 0, services.ErrCustom
	}
	e, err := s.d.Jobs.EnqueueNow(ctx, refreshRequest(serviceID, actor))
	return e.ID, err
}

// RefreshList queues a refresh of every upstream service of a list (Refresh
// all) and returns how many.
func (s *Service) RefreshList(ctx context.Context, listID int64, actor string) (int, error) {
	if _, err := s.d.Lists.Get(ctx, listID); err != nil {
		return 0, err
	}
	ids, err := store.UpstreamMembers(ctx, s.d.DB.R, listID)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, id := range ids {
			if _, err := s.d.Jobs.Enqueue(ctx, tx, refreshRequest(id, actor)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.d.Jobs.Kick()
	return len(ids), nil
}

// ActiveJob is the refresh job of a service that is queued or running (0: none).
func (s *Service) ActiveJob(ctx context.Context, serviceID int64) (int64, error) {
	held, err := s.d.Jobs.Holding(ctx, resourceKey(serviceID))
	if err != nil {
		return 0, err
	}
	for _, h := range held {
		if h.Type == JobRefresh {
			return h.ID, nil
		}
	}
	return 0, nil
}

// JobTypes are routing.refresh and routing.refresh_round.
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{
		{Name: JobRefresh, Queue: jobs.Refresh, MaxAttempts: 1, Steps: []jobs.Step{{Name: "refresh", Run: s.runRefresh}}},
		{Name: JobRound, Queue: jobs.Refresh, MaxAttempts: 1, Steps: []jobs.Step{
			{Name: "refresh", Run: s.runRound},
			{Name: "digest", Run: s.runDigest},
		}},
	}
}

// Schedules: the daily round at routing.refresh_at.
func (s *Service) Schedules() []jobs.Schedule {
	return []jobs.Schedule{{
		Name: JobRound, At: "04:00", Setting: conf.RefreshAt, Jitter: 5 * time.Minute,
		Request: func(context.Context) (jobs.Request, error) {
			return jobs.Request{Type: JobRound, CoalescingKey: "round", Subject: RoundSubject}, nil
		},
	}}
}

func (s *Service) runRefresh(ctx context.Context, r *jobs.Run) error {
	var p refreshPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	out, err := s.Refresh(ctx, p.ServiceID, false, r.Info().Actor())
	if errors.Is(err, services.ErrNotFound) || errors.Is(err, services.ErrCustom) {
		r.Log().Info("service %d is gone or custom: nothing to refresh", p.ServiceID)
		return nil
	}
	if err != nil {
		return err
	}
	logOutcome(r, out)
	return nil
}

func logOutcome(r *jobs.Run, out Outcome) {
	switch out.Result {
	case Failed:
		r.Log().Warn("%s: failed: %s", out.Tag, sources.ErrorText(out.Err))
	case Same:
		r.Log().Info("%s: unchanged", out.Tag)
	default:
		r.Log().Info("%s: %s (+%d −%d)", out.Tag, out.Result, out.Added, out.Removed)
	}
}

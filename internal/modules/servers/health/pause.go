package health

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Pause stops the checks of servers for d, or until resumed. In one write per
// server: the queued resume job of an earlier pause is cancelled, the server
// is `paused` at once and a resume job is queued for the end (it carries no
// resource key: a delayed job holding server:<id> would make every round skip
// the server for the whole pause).
func (s *Service) Pause(ctx context.Context, ids []int64, d time.Duration, untilResumed bool, actor string) error {
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := db.At(s.now())
		for _, id := range ids {
			h, err := store.GetHealth(ctx, tx, id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if h.State != "active" {
				continue
			}
			if _, err := s.Jobs.CancelQueued(ctx, tx, JobResume, store.ServerSubject(id), actor); err != nil {
				return err
			}
			until := store.PausedForever
			if !untilResumed {
				until = db.At(now.Add(d))
			}
			if err := store.SetPaused(ctx, tx, id, until); err != nil {
				return err
			}
			reason := Reason{Key: "health.reason.paused_forever"}
			if !untilResumed {
				reason = Reason{Key: "health.reason.paused", Args: map[string]any{"until": until.UTC().Format(time.RFC3339)}}
			}
			o := Outcome{Candidate: Paused, Reason: reason, Summary: map[string]any{"rule": "1"}}
			if h.Health != Paused {
				if err := s.recordChange(ctx, tx, id, h.Health, o, now, actor); err != nil {
					return err
				}
			} else if err := store.SetHealthState(ctx, tx, id, Paused, h.Since, reason.String()); err != nil {
				return err
			}
			ev := map[string]any{"until_resumed": untilResumed}
			if !untilResumed {
				ev["until"] = until.Format(db.TimeLayout)
			}
			if _, err := s.Events.Record(ctx, tx, events.Event{Type: "server.checks_paused", Subject: store.ServerSubject(id), Actor: actor, Payload: ev}); err != nil {
				return err
			}
			if !untilResumed {
				if _, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
					Type: JobResume, Subject: store.ServerSubject(id), Delay: d, Payload: payload{ServerID: id}, CreatedBy: actor,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		s.Jobs.Kick()
	}
	return err
}

// Resume ends a pause by hand: the queued resume job is cancelled so it cannot
// end a later pause, and the server is `unknown` until new results arrive.
func (s *Service) Resume(ctx context.Context, id int64, actor string) error {
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := s.Jobs.CancelQueued(ctx, tx, JobResume, store.ServerSubject(id), actor); err != nil {
			return err
		}
		return s.resumeTx(ctx, tx, id, actor, "admin")
	})
	if err == nil {
		s.Jobs.Kick()
	}
	return err
}

// resumeTx ends a pause in tx and queues a round of checks.
func (s *Service) resumeTx(ctx context.Context, tx *sqlx.Tx, id int64, actor, by string) error {
	h, err := store.GetHealth(ctx, tx, id)
	if err != nil {
		return err
	}
	if h.State != "active" || h.PausedUntil.IsZero() {
		return nil
	}
	now := db.At(s.now())
	if err := store.ClearPaused(ctx, tx, id); err != nil {
		return err
	}
	o := Outcome{Candidate: Unknown, Reason: Reason{Key: "health.reason.waiting"}, Summary: map[string]any{"rule": "resume"}}
	if err := s.recordChange(ctx, tx, id, h.Health, o, now, actor); err != nil {
		return err
	}
	if _, err := s.Events.Record(ctx, tx, events.Event{Type: "server.checks_resumed", Subject: store.ServerSubject(id), Actor: actor,
		Payload: map[string]any{"by": by}}); err != nil {
		return err
	}
	_, err = s.enqueueRound(ctx, tx, id, actor, s.ProxyDelay)
	return err
}

// resumeStep is the end of a timed pause. It resumes only when the stored end
// has passed: a later pause or a manual resume has already decided otherwise.
func (s *Service) resumeStep(ctx context.Context, r *jobs.Run) error {
	p, err := s.payloadOf(r)
	if err != nil {
		return err
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		h, err := store.GetHealth(ctx, tx, p.ServerID)
		if err != nil {
			return err
		}
		if h.PausedUntil.IsZero() || h.PausedUntil.After(s.now()) {
			r.Log().Info("Nothing to resume: the pause was changed")
			return nil
		}
		r.Log().Info("The pause is over")
		return s.resumeTx(ctx, tx, p.ServerID, r.Info().Actor(), "timer")
	})
}

// RunResult says what Run checks now queued.
type RunResult struct {
	// Busy: a job is running on the server, so no check was queued.
	Busy bool
	// OverBudget: the external check was refused by its limits.
	OverBudget bool
}

// RunNow queues a full round for one server at once: the self-check, the proxy
// test and an on-demand external check.
func (s *Service) RunNow(ctx context.Context, id int64, actor string) (RunResult, error) {
	var res RunResult
	cfg, err := s.Config(ctx)
	if err != nil {
		return res, err
	}
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		h, err := store.GetHealth(ctx, tx, id)
		if err != nil {
			return err
		}
		if h.State != "active" || h.Retiring {
			return ErrNotActive
		}
		res.Busy, err = s.enqueueRound(ctx, tx, id, actor, 0)
		if err != nil || res.Busy {
			return err
		}
		ok, _, err := s.budget(ctx, tx, id, ExternalOnDemand, cfg, s.now())
		if err != nil {
			return err
		}
		if !ok {
			res.OverBudget = true
			return nil
		}
		_, err = s.enqueueExternal(ctx, tx, id, ExternalOnDemand, 0, actor)
		return err
	})
	if err == nil {
		s.Jobs.Kick()
	}
	return res, err
}

// ErrNotActive is returned for a server that has no health.
var ErrNotActive = errors.New("health: the server is not active")

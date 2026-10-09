package discovery

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// JobDiscover is a discovery run's job.
const JobDiscover = "routing.discover"

type payload struct {
	RunID int64 `json:"run_id"`
}

// JobTypes is routing.discover: queue discovery (one run at a time), one
// attempt, steps lookup, visit and finish.
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{{
		Name: JobDiscover, Queue: jobs.Discovery, MaxAttempts: 1,
		Steps: []jobs.Step{
			{Name: "lookup", Run: s.stepLookup},
			{Name: "visit", Run: s.stepVisit},
			{Name: "finish", Run: s.stepFinish},
		},
		OnFailed: s.onFailed,
		OnCancelled: func(ctx context.Context, tx *sqlx.Tx, j jobs.Info, _ string) error {
			r, err := store.RunOfJob(ctx, tx, j.ID)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			return store.FinishRun(ctx, tx, r.ID, Failed, "cancelled", db.At(s.d.Now()))
		},
	}}
}

// onFailed marks the run failed and records routing.discovery_failed
// instead of job.failed. The suggestions stay on the run.
func (s *Service) onFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	r, err := store.RunOfJob(ctx, tx, j.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	text := cause.Error()
	if len(text) > 500 {
		text = text[:500]
	}
	if err := store.FinishRun(ctx, tx, r.ID, Failed, text, db.At(s.d.Now())); err != nil {
		return err
	}
	_, err = s.d.Events.Record(ctx, tx, events.Event{
		Type: "routing.discovery_failed", Actor: j.Actor(), Subject: Subject(r.ID),
		Payload: map[string]any{"website": r.Host, "error": text},
	})
	return err
}

func (s *Service) load(ctx context.Context, r *jobs.Run) (Run, error) {
	var p payload
	if err := r.Payload(&p); err != nil {
		return Run{}, jobs.Permanent(err)
	}
	row, err := store.GetRun(ctx, s.d.DB.R, p.RunID)
	if errors.Is(err, store.ErrNotFound) {
		return Run{}, jobs.Permanent(ErrNotFound)
	}
	if err != nil {
		return Run{}, err
	}
	return fromRow(row)
}

// stepLookup stores the catalog's suggestions in its own write, so the run
// page shows them before the visit ends.
func (s *Service) stepLookup(ctx context.Context, r *jobs.Run) error {
	run, err := s.load(ctx, r)
	if err != nil {
		return err
	}
	start := s.d.Now()
	list, err := s.d.Catalog.Lookup(ctx, run.Host, run.Registrable)
	if err != nil {
		return err
	}
	text, err := suggestionsJSON(list)
	if err != nil {
		return err
	}
	r.Log().Info("%d suggestions from the catalog for %s (%s)", len(list), run.Host, s.d.Now().Sub(start))
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.RunLookedUp(ctx, tx, run.ID, text) })
}

// stepFinish ends the run and records routing.discovery_completed.
func (s *Service) stepFinish(ctx context.Context, r *jobs.Run) error {
	run, err := s.load(ctx, r)
	if err != nil {
		return err
	}
	hosts, err := store.RunHosts(ctx, s.d.DB.R, run.ID)
	if err != nil {
		return err
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := store.FinishRun(ctx, tx, run.ID, Done, "", db.At(s.d.Now())); err != nil {
			return err
		}
		_, err := s.d.Events.Record(ctx, tx, events.Event{
			Type: "routing.discovery_completed", Actor: r.Info().Actor(), Subject: Subject(run.ID),
			Payload: map[string]any{"website": run.Host, "hosts": len(hosts), "suggestions": len(run.Suggestions)},
		})
		return err
	})
}

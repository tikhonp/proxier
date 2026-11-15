package aicheck

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

const JobCheck = "servers.ai_check"
const JobRound = "servers.ai_round"

func request(id int64, actor string) jobs.Request {
	key := "server:" + strconv.FormatInt(id, 10)
	return jobs.Request{Type: JobCheck, ResourceKey: key, CoalescingKey: key, CreatedBy: actor, Payload: map[string]any{"server_id": id}}
}

func (s *Service) Queue(ctx context.Context, id int64, actor string) (int64, error) {
	on, err := s.Enabled(ctx)
	if err != nil {
		return 0, err
	}
	if !on {
		return 0, ErrDisabled
	}
	var job jobs.Enqueued
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		srv, err := store.GetServer(ctx, tx, id)
		if err != nil {
			return err
		}
		if srv.State != "active" || srv.Retiring() {
			return ErrNotActive
		}
		job, err = s.Jobs.Enqueue(ctx, tx, request(id, actor))
		return err
	})
	if err == nil {
		s.Jobs.Kick()
	}
	return job.ID, err
}

func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{
		{Name: JobCheck, Queue: jobs.Checks, MaxAttempts: 1, Quiet: true, NoResume: true, Steps: []jobs.Step{{Name: "check", Run: func(ctx context.Context, r *jobs.Run) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() {
				select {
				case <-r.Cancelling():
					cancel()
				case <-ctx.Done():
				}
			}()
			var p struct {
				ServerID int64 `json:"server_id"`
			}
			if err := r.Payload(&p); err != nil {
				return err
			}
			err := s.Check(ctx, p.ServerID, r.Info().Actor())
			if errors.Is(err, ErrDisabled) || errors.Is(err, ErrNotActive) {
				return nil
			}
			if ctx.Err() != nil {
				return jobs.ErrCancelled
			}
			return err
		}}}},
		{Name: JobRound, Queue: jobs.Maintenance, MaxAttempts: 1, Quiet: true, Steps: []jobs.Step{{Name: "enqueue", Run: func(ctx context.Context, r *jobs.Run) error {
			return s.Round(ctx, r.Info().Actor())
		}}}},
	}
}

func (s *Service) Schedule() jobs.Schedule {
	return jobs.Schedule{Name: JobRound, Every: 24 * time.Hour, Jitter: 15 * time.Minute, Request: func(context.Context) (jobs.Request, error) {
		return jobs.Request{Type: JobRound, CoalescingKey: JobRound}, nil
	}}
}

// Round always expires old history, even while lookups are disabled.
func (s *Service) Round(ctx context.Context, actor string) error {
	if err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		n, err := store.PruneAI(ctx, tx, db.At(s.Now().Add(-30*24*time.Hour)))
		if err != nil || n == 0 {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Time: db.At(s.Now()), Type: "server.ai_history_pruned", Subject: events.Subject{Type: "home", ID: "1"}, Actor: actor, Payload: map[string]any{"rows": n}})
		return err
	}); err != nil {
		return err
	}
	on, err := s.Enabled(ctx)
	if err != nil || !on {
		return err
	}
	daily, err := s.Settings.GetBool(ctx, conf.AIDaily)
	if err != nil || !daily {
		return err
	}
	all, err := store.ListServers(ctx, s.DB.R)
	if err != nil {
		return err
	}
	for _, srv := range all {
		if srv.State != "active" || srv.Retiring() {
			continue
		}
		err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			req := request(srv.ID, actor)
			req.SkipIfBusy = true
			_, err := s.Jobs.Enqueue(ctx, tx, req)
			return err
		})
		if err != nil && !errors.Is(err, jobs.ErrSkipped) {
			return err
		}
	}
	s.Jobs.Kick()
	return nil
}

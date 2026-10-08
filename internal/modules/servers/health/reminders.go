package health

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// remindersStep records server.still_unhealthy for servers that have been
// blocked or down for the reminder interval since the last reminder (or since
// the state began).
func (s *Service) remindersStep(ctx context.Context, r *jobs.Run) error {
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		list, err := store.ListHealth(ctx, tx)
		if err != nil {
			return err
		}
		now := db.At(s.now())
		for _, h := range list {
			if (h.Health != Blocked && h.Health != Down) || h.Since.IsZero() || h.Retiring {
				continue
			}
			ref := h.Since
			if !h.RemindedAt.IsZero() {
				ref = h.RemindedAt
			}
			if now.Sub(ref.Time) < cfg.ReminderEvery {
				continue
			}
			if _, err := s.Events.Record(ctx, tx, events.Event{
				Type: "server.still_unhealthy", Subject: store.ServerSubject(h.ServerID), Actor: r.Info().Actor(),
				Payload: map[string]any{"state": h.Health, "since": h.Since.Format(db.TimeLayout)},
			}); err != nil {
				return err
			}
			if err := store.SetReminded(ctx, tx, h.ServerID, now); err != nil {
				return err
			}
			r.Log().Info("Reminder: server %d is still %s", h.ServerID, h.Health)
		}
		return nil
	})
}

func (s *Service) rollupStep(ctx context.Context, r *jobs.Run) error {
	if err := s.Stats.Rollup(ctx); err != nil {
		return err
	}
	r.Log().Info("Rolled up samples and pruned old results")
	return nil
}

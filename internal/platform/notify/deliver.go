package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

const deliverType = "platform.notify"

// JobType is the delivery job: one step, retried after 10 s, 1 min and 5 min.
// Its failure is the notification's, never a job.failed (which would notify
// about the failure of a notification).
func (s *Service) JobType() jobs.Type {
	return jobs.Type{
		Name: deliverType, Queue: jobs.Notify, MaxAttempts: 4,
		Backoff:  []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute},
		Steps:    []jobs.Step{{Name: "send", Run: s.send}},
		OnFailed: func(ctx context.Context, tx *sqlx.Tx, j jobs.Info, err error) error { return s.giveUp(ctx, tx, j, err) },
		OnCancelled: func(ctx context.Context, tx *sqlx.Tx, j jobs.Info, by string) error {
			return s.giveUp(ctx, tx, j, errors.New("cancelled by "+by))
		},
	}
}

func (s *Service) send(ctx context.Context, r *jobs.Run) error {
	var p struct {
		ID int64 `json:"notification_id"`
	}
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	// The retry of a job is a new job: this one owns the notification now.
	if err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE notifications SET job_id = ? WHERE id = ?`, r.Info().ID, p.ID)
		return err
	}); err != nil {
		return err
	}
	n, err := s.Get(ctx, p.ID)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("notification %d: %w", p.ID, err))
	}
	if n.State == Sent {
		r.Log().Info("Already sent")
		return nil
	}
	ch := s.channel(n.Channel)
	if ch == nil {
		return jobs.Permanent(fmt.Errorf("no channel %q", n.Channel))
	}
	r.Log().Info("Sending to %s: %s", n.Channel, n.Message.Title)
	err = ch.Send(ctx, n.Message)
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		// Asked to wait: not a failed try.
		return jobs.Defer(rl.RetryAfter, "the channel asked to wait")
	}
	if err != nil {
		if werr := s.count(ctx, n.ID, err); werr != nil {
			return werr
		}
		return err
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE notifications SET state = 'sent', attempts = attempts + 1, last_error = '', sent_at = ? WHERE id = ?`,
			db.At(s.Now()), n.ID)
		return err
	})
}

// count notes a failed try.
func (s *Service) count(ctx context.Context, id int64, cause error) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE notifications SET attempts = attempts + 1, last_error = ? WHERE id = ?`, cut(cause.Error(), 500), id)
		return err
	})
}

// giveUp marks the notification failed and records why, in the transaction
// that ends the job. It returns nil even when nothing matched, so the platform
// does not record a job.failed for it.
func (s *Service) giveUp(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	var n struct {
		ID      int64  `db:"id"`
		EventID int64  `db:"event_id"`
		Channel string `db:"channel"`
	}
	err := tx.GetContext(ctx, &n, `
		UPDATE notifications SET state = 'failed', last_error = ? WHERE job_id = ? RETURNING id, event_id, channel`,
		cut(cause.Error(), 500), j.ID)
	if err != nil {
		s.log.Error("notify: giving up", "job", j.ID, "error", err)
		return nil
	}
	_, err = s.ev.Record(ctx, tx, events.Event{
		Type: FailedEvent.Name, Actor: j.Actor(), Subject: events.Subject{Type: "event", ID: fmt.Sprint(n.EventID)},
		Payload: map[string]any{"channel": n.Channel, "error": cut(cause.Error(), 500)},
	})
	return err
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

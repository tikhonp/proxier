package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
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
		Merge:    mergeGroup,
		OnFailed: func(ctx context.Context, tx *sqlx.Tx, j jobs.Info, err error) error { return s.giveUp(ctx, tx, j, err) },
		OnCancelled: func(ctx context.Context, tx *sqlx.Tx, j jobs.Info, by string) error {
			return s.giveUp(ctx, tx, j, errors.New("cancelled by "+by))
		},
	}
}

// GroupWindow is how long the first notification of a group waits for the
// rest of its burst.
const GroupWindow = 2 * time.Minute

// deliverPayload is the delivery job's payload: one notification, or a
// group's (IDs, with the event type and group key its title is chosen by).
type deliverPayload struct {
	ID   int64   `json:"notification_id,omitempty"`
	IDs  []int64 `json:"notification_ids,omitempty"`
	Type string  `json:"type,omitempty"`
	Key  string  `json:"key,omitempty"`
}

func (p deliverPayload) ids() []int64 {
	if len(p.IDs) > 0 {
		return p.IDs
	}
	return []int64{p.ID}
}

// mergeGroup adds a coalesced notification to the group waiting to be sent.
func mergeGroup(queued, incoming json.RawMessage) (json.RawMessage, error) {
	var q, in deliverPayload
	if err := json.Unmarshal(queued, &q); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(incoming, &in); err != nil {
		return nil, err
	}
	for _, id := range in.ids() {
		if !slices.Contains(q.IDs, id) {
			q.IDs = append(q.IDs, id)
		}
	}
	return json.Marshal(q)
}

func (s *Service) send(ctx context.Context, r *jobs.Run) error {
	var p deliverPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	ids := p.ids()
	// The retry of a job is a new job: this one owns the notifications now.
	if err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
		q, args, err := sqlx.In(`UPDATE notifications SET job_id = ? WHERE id IN (?)`, r.Info().ID, ids)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, q, args...)
		return err
	}); err != nil {
		return err
	}
	var todo []Notification
	for _, id := range ids {
		n, err := s.Get(ctx, id)
		if errors.Is(err, sql.ErrNoRows) && len(ids) > 1 {
			continue // pruned while its group waited
		}
		if err != nil {
			return jobs.Permanent(fmt.Errorf("notification %d: %w", id, err))
		}
		if n.State != Sent {
			todo = append(todo, n)
		}
	}
	if len(todo) == 0 {
		r.Log().Info("Already sent")
		return nil
	}
	first := todo[0]
	ch := s.channel(first.Channel)
	if ch == nil {
		return jobs.Permanent(fmt.Errorf("no channel %q", first.Channel))
	}
	// The address is read now, so a chat changed while this waited applies;
	// a member removed meanwhile gets nothing.
	m, err := s.a.Member(ctx, first.MemberID)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("recipient %d: %w", first.MemberID, err))
	}
	to := ch.Address(m)
	switch {
	case m.Removed():
		return jobs.Permanent(fmt.Errorf("%s was removed", m.Username))
	case to == "":
		return jobs.Permanent(fmt.Errorf("%s has no %s chat", m.Username, first.Channel))
	}
	msg := first.Message
	if len(todo) > 1 {
		var err error
		if msg, err = s.groupMessage(ctx, p, todo); err != nil {
			return err
		}
	}
	sending := make([]int64, len(todo))
	for i, n := range todo {
		sending[i] = n.ID
	}
	r.Log().Info("Sending to %s: %s", first.Channel, msg.Title)
	err = ch.Send(ctx, to, msg)
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		// Asked to wait: not a failed try.
		return jobs.Defer(rl.RetryAfter, "the channel asked to wait")
	}
	if err != nil {
		if werr := s.count(ctx, sending, err); werr != nil {
			return werr
		}
		return err
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		q, args, err := sqlx.In(`
			UPDATE notifications SET state = 'sent', attempts = attempts + 1, last_error = '', sent_at = ? WHERE id IN (?)`,
			db.At(s.Now()), sending)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, q, args...)
		return err
	})
}

// groupMessage is one message for a group: a counted title in the first
// notification's language and each notification's text on its own line.
func (s *Service) groupMessage(ctx context.Context, p deliverPayload, ns []Notification) (Message, error) {
	var lang string
	if err := s.d.R.GetContext(ctx, &lang, `SELECT lang FROM notifications WHERE id = ?`, ns[0].ID); err != nil {
		return Message{}, err
	}
	loc := s.cat.Localizer(i18n.Lang(lang), s.zone(ctx))
	key := "notify." + p.Type + ".group." + p.Key
	if !loc.Has(key) {
		key = "notify.group"
	}
	lines := make([]string, len(ns))
	for i, n := range ns {
		lines[i] = n.Message.Title
		if n.Message.Body != "" {
			lines[i] += ": " + n.Message.Body
		}
	}
	href := ""
	if t, ok := s.ev.Lookup(p.Type); ok {
		href = t.GroupHref
	}
	m := Message{Emoji: ns[0].Message.Emoji, Title: loc.N(key, int64(len(ns))), Body: strings.Join(lines, "\n"), URL: s.link(href)}
	if m.URL != "" {
		m.Button = loc.T("notify.open")
	}
	return m, nil
}

// count notes a failed try.
func (s *Service) count(ctx context.Context, ids []int64, cause error) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		q, args, err := sqlx.In(`
			UPDATE notifications SET attempts = attempts + 1, last_error = ? WHERE id IN (?)`, cut(cause.Error(), 500), ids)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, q, args...)
		return err
	})
}

// giveUp marks the notification failed and records why, in the transaction
// that ends the job. It returns nil even when nothing matched, so the platform
// does not record a job.failed for it.
func (s *Service) giveUp(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	var n struct {
		ID       int64  `db:"id"`
		EventID  int64  `db:"event_id"`
		Channel  string `db:"channel"`
		MemberID int64  `db:"member_id"`
	}
	err := tx.GetContext(ctx, &n, `
		UPDATE notifications SET state = 'failed', last_error = ? WHERE job_id = ? RETURNING id, event_id, channel, member_id`,
		cut(cause.Error(), 500), j.ID)
	if err != nil {
		s.log.Error("notify: giving up", "job", j.ID, "error", err)
		return nil
	}
	to := events.MemberActor(n.MemberID)
	if m, err := s.a.Member(ctx, n.MemberID); err == nil {
		to = m.Username
	}
	_, err = s.ev.Record(ctx, tx, events.Event{
		Type: FailedEvent.Name, Actor: j.Actor(), Subject: events.Subject{Type: "event", ID: fmt.Sprint(n.EventID)},
		Payload: map[string]any{"channel": n.Channel, "to": to, "error": cut(cause.Error(), 500)},
	})
	return err
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

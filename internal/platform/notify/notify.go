// Package notify tells members about events that need attention
// (docs/processes/platform/notifications.md). The notifier is an event
// subscriber: for a committed event it finds the members in its audience
// who have its rule on, renders the message in each one's language and
// queues one delivery job each. A channel (Telegram) sends it to the
// member's own address there. Notifications live in their own table; only a
// failed delivery is an event, and it is never notified about.
package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// FailedEvent is recorded when a notification could not be delivered. It
// cannot itself be notified about.
var FailedEvent = events.Type{
	Name: "notification.failed", Module: "platform",
	Description: "A notification could not be delivered.",
	// Those from before members name no recipient.
	Variant: func(p map[string]any) string {
		if _, ok := p["to"]; ok {
			return "to"
		}
		return ""
	},
}

// Prefix of the event types that are never notifiable.
const eventPrefix = "notification."

// Message is a notification as rendered when queued. Plain text: the channel
// escapes it.
type Message struct {
	Emoji  string `json:"emoji"`
	Title  string `json:"title"`  // "nl-2 is ready"
	Body   string `json:"body"`   // one sentence, optional
	URL    string `json:"url"`    // absolute, the subject's page; "" for no button
	Button string `json:"button"` // "Open in Proxier", translated
}

// Channel delivers messages somewhere.
type Channel interface {
	Name() string // "telegram"
	// Configured reports whether the channel can send at all (Telegram: a
	// bot token).
	Configured(ctx context.Context) (bool, error)
	// Address is where m receives on this channel, "" for nowhere.
	Address(m auth.Member) string
	// Send delivers m to an address. *RateLimitedError asks to wait; other
	// errors are retried.
	Send(ctx context.Context, to string, m Message) error
}

// RateLimitedError asks the sender to wait before trying again.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
}

// Renderer is module.NotificationRenderer: a module's own texts for its
// types. It fills Title and Body; the service adds the emoji, the button and
// the link where it left them empty.
type Renderer interface {
	RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (Message, bool, error)
}

// SubjectNamer is module.SubjectNamer: the names and pages of subjects.
type SubjectNamer interface {
	SubjectTypes() []string
	NameSubjects(ctx context.Context, typ string, ids []string) (map[string]ui.SubjectRef, error)
}

// Rule is a notification rule of one event type.
type Rule struct {
	Type    events.Type
	Enabled bool
	Default bool
}

// Notification is a stored message to one member and how its delivery went.
type Notification struct {
	ID, EventID, JobID int64
	MemberID           int64 // the recipient
	Channel, State     string
	Message            Message
	Attempts           int
	LastError          string
	CreatedAt, SentAt  db.Time
}

// Notification states.
const (
	Queued = "queued"
	Sent   = "sent"
	Failed = "failed"
)

// ErrUnknownRule is returned for an event type that has no rule.
var ErrUnknownRule = errors.New("notify: no rule for this event type")

// ErrNotFailed is returned when retrying a notification that did not fail.
var ErrNotFailed = errors.New("notify: only failed notifications can be retried")

// Service is the notifier and its rules.
type Service struct {
	// Now is the clock for sent times; tests replace it.
	Now func() time.Time

	d    *db.DB
	ev   *events.Catalog
	cat  *i18n.Catalog
	j    *jobs.System
	a    *auth.Service
	st   *settings.Store
	base *url.URL
	log  *slog.Logger

	mu        sync.RWMutex
	channels  []Channel
	renderers map[string]Renderer
	namers    map[string]SubjectNamer
	owners    []SubjectOwner
}

// New returns a service with no channels. st supplies the display time zone.
func New(d *db.DB, ev *events.Catalog, cat *i18n.Catalog, j *jobs.System, a *auth.Service, st *settings.Store,
	baseURL *url.URL, log *slog.Logger) *Service {
	return &Service{Now: time.Now, d: d, ev: ev, cat: cat, j: j, a: a, st: st, base: baseURL, log: log,
		renderers: map[string]Renderer{}, namers: map[string]SubjectNamer{}}
}

// AddChannel registers a channel; call it before the dispatcher starts.
func (s *Service) AddChannel(c Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.channels {
		if o.Name() == c.Name() {
			return fmt.Errorf("notify: channel %q registered twice", c.Name())
		}
	}
	s.channels = append(s.channels, c)
	return nil
}

// AddRenderer registers the renderer of a module's event types.
func (s *Service) AddRenderer(module string, r Renderer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.renderers[module]; dup {
		return fmt.Errorf("notify: renderer of %q registered twice", module)
	}
	s.renderers[module] = r
	return nil
}

// AddNamer registers a namer for its subject types.
func (s *Service) AddNamer(n SubjectNamer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range n.SubjectTypes() {
		if _, dup := s.namers[t]; dup {
			return fmt.Errorf("notify: subject type %q named twice", t)
		}
		s.namers[t] = n
	}
	return nil
}

func (s *Service) channel(name string) Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.channels {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func (s *Service) allChannels() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Channel(nil), s.channels...)
}

// Configured reports whether any channel can send.
func (s *Service) Configured(ctx context.Context) (bool, error) {
	for _, c := range s.allChannels() {
		ok, err := c.Configured(ctx)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// Rules lists a member's rule of every event type they may get, by name:
// those of areas the grant g doesn't reach are not theirs to choose.
func (s *Service) Rules(ctx context.Context, member int64, g access.Grant) ([]Rule, error) {
	var rows []struct {
		Type    string `db:"event_type"`
		Enabled bool   `db:"enabled"`
	}
	if err := s.d.R.SelectContext(ctx, &rows, `SELECT event_type, enabled FROM notification_rules WHERE member_id = ?`, member); err != nil {
		return nil, fmt.Errorf("notify: rules: %w", err)
	}
	set := map[string]bool{}
	for _, r := range rows {
		set[r.Type] = r.Enabled
	}
	var out []Rule
	for _, t := range s.ev.Types() {
		if strings.HasPrefix(t.Name, eventPrefix) || !mayReceive(t, g) {
			continue
		}
		on, changed := set[t.Name]
		if !changed {
			on = t.Notify
		}
		out = append(out, Rule{Type: t, Enabled: on, Default: t.Notify})
	}
	return out, nil
}

// SetRule turns one of a member's rules on or off. A rule back at its
// default deletes its row, so a changed default reaches whoever never
// touched it. A change records member.notifications_changed.
func (s *Service) SetRule(ctx context.Context, member int64, g access.Grant, eventType string, enabled bool) error {
	t, ok := s.ev.Lookup(eventType)
	if !ok || strings.HasPrefix(eventType, eventPrefix) || !mayReceive(t, g) {
		return fmt.Errorf("%w: %q", ErrUnknownRule, eventType)
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		cur := t.Notify
		var stored bool
		switch err := tx.GetContext(ctx, &stored, `SELECT enabled FROM notification_rules WHERE member_id = ? AND event_type = ?`, member, eventType); {
		case err == nil:
			cur = stored
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if cur == enabled {
			return nil
		}
		var err error
		if enabled == t.Notify {
			_, err = tx.ExecContext(ctx, `DELETE FROM notification_rules WHERE member_id = ? AND event_type = ?`, member, eventType)
		} else {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO notification_rules (member_id, event_type, enabled, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (member_id, event_type) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
				member, eventType, enabled, db.At(s.Now()))
		}
		if err != nil {
			return err
		}
		_, err = s.ev.Record(ctx, tx, events.Event{
			Type: auth.NotificationsChangedEvent, Actor: events.MemberActor(member), Subject: auth.MemberSubject(member),
			Payload: map[string]any{"keys": "notifications." + eventType},
		})
		return err
	})
}

const notificationColumns = `id, event_id, COALESCE(job_id, 0) AS job_id, channel, member_id, state, message, attempts, last_error, created_at, sent_at`

type notificationRow struct {
	ID        int64   `db:"id"`
	EventID   int64   `db:"event_id"`
	JobID     int64   `db:"job_id"`
	Channel   string  `db:"channel"`
	MemberID  int64   `db:"member_id"`
	State     string  `db:"state"`
	Message   string  `db:"message"`
	Attempts  int     `db:"attempts"`
	LastError string  `db:"last_error"`
	CreatedAt db.Time `db:"created_at"`
	SentAt    db.Time `db:"sent_at"`
}

func (r notificationRow) notification() (Notification, error) {
	n := Notification{ID: r.ID, EventID: r.EventID, JobID: r.JobID, Channel: r.Channel, MemberID: r.MemberID, State: r.State,
		Attempts: r.Attempts, LastError: r.LastError, CreatedAt: r.CreatedAt, SentAt: r.SentAt}
	if err := unmarshalMessage(r.Message, &n.Message); err != nil {
		return Notification{}, fmt.Errorf("notify: message of notification %d: %w", r.ID, err)
	}
	return n, nil
}

// Get returns one notification.
func (s *Service) Get(ctx context.Context, id int64) (Notification, error) {
	var r notificationRow
	if err := s.d.R.GetContext(ctx, &r, `SELECT `+notificationColumns+` FROM notifications WHERE id = ?`, id); err != nil {
		return Notification{}, err
	}
	return r.notification()
}

// Failed lists a member's notifications that failed since the given time,
// newest first.
func (s *Service) Failed(ctx context.Context, member int64, since time.Time, limit int) ([]Notification, error) {
	var rows []notificationRow
	if err := s.d.R.SelectContext(ctx, &rows, `
		SELECT `+notificationColumns+` FROM notifications
		WHERE member_id = ? AND state = 'failed' AND created_at >= ? ORDER BY id DESC LIMIT ?`, member, db.At(since), limit); err != nil {
		return nil, fmt.Errorf("notify: failed list: %w", err)
	}
	out := make([]Notification, 0, len(rows))
	for _, r := range rows {
		n, err := r.notification()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// LatestFailed reports whether a member's newest finished notification
// failed. It is the header's "Telegram failing".
func (s *Service) LatestFailed(ctx context.Context, member int64) (bool, error) {
	var state string
	err := s.d.R.GetContext(ctx, &state, `SELECT state FROM notifications WHERE member_id = ? AND state != 'queued' ORDER BY id DESC LIMIT 1`, member)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return state == Failed, err
}

// Retry sends a member's failed notification again, as a new linked job; a
// grouped one goes again with its whole group. Another member's is not
// theirs to retry.
func (s *Service) Retry(ctx context.Context, member, id int64) error {
	n, err := s.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFailed
	}
	if err != nil {
		return err
	}
	if n.State != Failed || n.JobID == 0 || n.MemberID != member {
		return ErrNotFailed
	}
	newID, err := s.j.Retry(ctx, n.JobID, events.MemberActor(member))
	if err != nil {
		return err
	}
	// The guard keeps a worker that already sent it from being undone.
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE notifications SET state = 'queued', job_id = ?, last_error = '' WHERE job_id IN (?, ?) AND state = 'failed'`, newID, newID, n.JobID)
		return err
	})
}

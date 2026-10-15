package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

// SubscriberName is the notifier's cursor key.
const SubscriberName = "platform.notifier"

func unmarshalMessage(s string, m *Message) error { return json.Unmarshal([]byte(s), m) }

// Subscriber is the notifier: it reacts to every committed event.
func (s *Service) Subscriber() events.Subscriber {
	return events.Subscriber{Name: SubscriberName, Handle: s.handle}
}

// handle runs in the transaction that advances the notifier's cursor, so
// each recipient's notification and its job are queued exactly once; the
// unique key still guards a replay. Each recipient gets their own row and
// job, so one chat failing neither holds up nor repeats another's.
func (s *Service) handle(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
	// A notification about a failed notification would loop.
	if strings.HasPrefix(e.Type, eventPrefix) || (e.Type == "job.failed" && e.Payload["type"] == deliverType) {
		return nil
	}
	t, ok := s.ev.Lookup(e.Type)
	if !ok || (t.NotifyIf != nil && !t.NotifyIf(e.Payload)) {
		return nil
	}
	var chans []Channel
	for _, c := range s.allChannels() {
		// Nothing is queued while the channel is not set up, and enabling it
		// later sends no backlog of stale alarms.
		ok, err := c.Configured(ctx)
		if err != nil {
			return err
		}
		if ok {
			chans = append(chans, c)
		}
	}
	if len(chans) == 0 {
		return nil
	}
	to, err := s.recipients(ctx, tx, e, t)
	if err != nil {
		return err
	}
	byLang := map[i18n.Lang]Message{}
	for _, c := range chans {
		for _, m := range to {
			// A member with no address there gets nothing, and nothing waits
			// for them to choose one.
			if c.Address(m) == "" {
				continue
			}
			lang := s.language(m)
			msg, done := byLang[lang]
			if !done {
				msg = s.render(ctx, e, t, lang)
				byLang[lang] = msg
			}
			if err := s.queue(ctx, tx, e, t, c, m.ID, lang, msg); err != nil {
				return err
			}
		}
	}
	return nil
}

// queue stores one recipient's notification and its delivery job. A replay
// of the event finds the row and queues nothing.
func (s *Service) queue(ctx context.Context, tx *sqlx.Tx, e events.Event, t events.Type, c Channel, member int64, lang i18n.Lang, m Message) error {
	msg, err := json.Marshal(m)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO notifications (event_id, channel, member_id, lang, message, state, created_at)
		VALUES (?, ?, ?, ?, ?, 'queued', ?)
		ON CONFLICT (event_id, channel, member_id) DO NOTHING`, e.ID, c.Name(), member, string(lang), string(msg), db.At(s.Now()))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	req := jobs.Request{
		Type: deliverType, Payload: map[string]any{"notification_id": id},
		CreatedBy: fmt.Sprintf("event:%d", e.ID),
	}
	if key := groupKey(t, e); key != "" {
		// The first of a recipient's group waits out the window; the others
		// merge into its job while it waits.
		req.Payload = deliverPayload{IDs: []int64{id}, Type: e.Type, Key: key}
		req.CoalescingKey = fmt.Sprintf("group:%s:%d:%s:%s", c.Name(), member, e.Type, key)
		req.Delay = GroupWindow
	}
	q, err := s.j.Enqueue(ctx, tx, req)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE notifications SET job_id = ? WHERE id = ?`, q.ID, id)
	return err
}

// language is the member's own, English when theirs isn't one there is.
func (s *Service) language(m auth.Member) i18n.Lang {
	if m.Language.Valid() {
		return m.Language
	}
	return i18n.EN
}

// render writes the message in a recipient's language, with the names of
// this moment. A text that cannot be built falls back to the event's type, so
// a missing translation never stalls the notifier's cursor.
func (s *Service) render(ctx context.Context, e events.Event, t events.Type, lang i18n.Lang) Message {
	loc := s.cat.Localizer(lang, s.zone(ctx))
	ctx = i18n.WithLocalizer(ctx, loc)
	ref := s.name(ctx, e.Subject)

	msg, err := s.compose(ctx, e, t, loc, ref)
	if err != nil {
		s.log.Error("notify: render", "event", e.ID, "type", e.Type, "error", err)
		msg = Message{Title: e.Type}
	}
	if msg.Emoji == "" {
		msg.Emoji = t.Emoji
	}
	if msg.URL == "" {
		msg.URL = s.link(ref.Href)
	}
	if msg.URL != "" && msg.Button == "" {
		msg.Button = loc.T("notify.open")
	}
	return msg
}

// zone is the display time zone, UTC when the setting can't be read.
func (s *Service) zone(ctx context.Context) *time.Location {
	if name, err := s.st.Get(ctx, "general.time_zone"); err == nil {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
	}
	return time.UTC
}

func (s *Service) compose(ctx context.Context, e events.Event, t events.Type, loc *i18n.Localizer, ref ui.SubjectRef) (Message, error) {
	s.mu.RLock()
	r := s.renderers[e.Module]
	s.mu.RUnlock()
	if r != nil {
		m, ok, err := r.RenderNotification(ctx, e, loc)
		if err != nil {
			return Message{}, err
		}
		if ok {
			return m, nil
		}
	}
	args := i18n.Args{"subject": ref.Label, "actor": e.Actor}
	for k, v := range e.Payload {
		if _, taken := args[k]; !taken {
			args[k] = v
		}
	}
	key := "notify." + e.Type
	if !loc.Has(key) {
		return Message{}, fmt.Errorf("no text %q", key)
	}
	m := Message{Title: loc.T(key, args)}
	if loc.Has(key + ".body") {
		m.Body = loc.T(key+".body", args)
	}
	return m, nil
}

// link is the absolute address of a page of this instance. A subject without a
// page leads to Activity, where its event is.
func (s *Service) link(href string) string {
	if s.base == nil {
		return ""
	}
	if href == "" {
		href = "/activity"
	}
	return strings.TrimRight(s.base.String(), "/") + href
}

// name resolves a subject to its label and page, "type:id" when nobody names it.
func (s *Service) name(ctx context.Context, sub events.Subject) ui.SubjectRef {
	if sub.Type == "" {
		return ui.SubjectRef{}
	}
	s.mu.RLock()
	n := s.namers[sub.Type]
	s.mu.RUnlock()
	var named map[string]ui.SubjectRef
	var err error
	if n != nil {
		named, err = n.NameSubjects(ctx, sub.Type, []string{sub.ID})
	} else {
		named, err = s.platformNames(ctx, sub)
	}
	if err != nil {
		s.log.Error("notify: naming a subject", "subject", sub.String(), "error", err)
	}
	if ref, ok := named[sub.ID]; ok {
		return ref
	}
	return ui.SubjectRef{Label: sub.String()}
}

// platformNames names the platform's own subject types.
func (s *Service) platformNames(ctx context.Context, sub events.Subject) (map[string]ui.SubjectRef, error) {
	ref := ui.SubjectRef{}
	switch sub.Type {
	case "member", "admin": // admin:1 is the subject of events recorded before members
		ref = ui.SubjectRef{Label: sub.String(), Href: "/settings/security"}
		if m, err := s.a.Member(ctx, auth.SubjectMember(sub)); err == nil {
			ref.Label = m.Username
		}
	case "job":
		label := "#" + sub.ID
		var typ string
		if err := s.d.R.GetContext(ctx, &typ, `SELECT type FROM jobs WHERE id = ?`, sub.ID); err == nil {
			title := typ
			if k := "job." + typ; i18n.From(ctx).Has(k) {
				title = i18n.T(ctx, k)
			}
			label = i18n.T(ctx, "notify.job_name", i18n.Args{"id": sub.ID, "type": title})
		}
		ref = ui.SubjectRef{Label: label, Href: "/jobs/" + sub.ID}
	case "settings":
		ref = ui.SubjectRef{Label: sub.ID, Href: "/settings/" + sub.ID}
		if k := "settings." + sub.ID; i18n.From(ctx).Has(k) {
			ref.Label = i18n.T(ctx, k)
		}
		if sub.ID == "telegram" {
			ref.Href = "/settings/integrations/telegram"
		}
	case "schedule":
		ref = ui.SubjectRef{Label: sub.ID, Href: "/jobs?type=" + sub.ID}
	default:
		return nil, nil
	}
	return map[string]ui.SubjectRef{sub.ID: ref}, nil
}

func groupKey(t events.Type, e events.Event) string {
	if t.Group == nil {
		return ""
	}
	return t.Group(e.Payload)
}

package notify_test

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

var bg = context.Background()

const (
	token = "123456:TEST-token"
	chat  = "-1001"
)

// clock is the channel's clock: its Sleep moves it, so pacing is visible
// without waiting.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

// names is a SubjectNamer for "server" subjects the tests can rename.
type names struct {
	mu sync.Mutex
	by map[string]string
}

func (n *names) SubjectTypes() []string { return []string{"server"} }
func (n *names) NameSubjects(_ context.Context, _ string, ids []string) (map[string]ui.SubjectRef, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]ui.SubjectRef{}
	for _, id := range ids {
		if name, ok := n.by[id]; ok {
			out[id] = ui.SubjectRef{Label: name, Href: "/servers/" + id}
		}
	}
	return out, nil
}

func (n *names) rename(id, name string) { n.mu.Lock(); n.by[id] = name; n.mu.Unlock() }

// Event types the tests record. "test.server_down" notifies by default,
// "test.server_degraded" doesn't.
var testTypes = []events.Type{
	{Name: "test.server_down", Module: "test", Notify: true, Emoji: "🔴", Description: "A server is down."},
	{Name: "test.server_degraded", Module: "test", Emoji: "🟡", Description: "A server is degraded."},
	{Name: "test.custom", Module: "test", Notify: true, Emoji: "🧭", Description: "Rendered by its module."},
}

var testMessages = i18n.Messages{
	"notify.open":                  {EN: "Open in Proxier", RU: "Открыть в Proxier"},
	"notify.test.server_down":      {EN: "{subject} is down", RU: "{subject} недоступен"},
	"notify.test.server_down.body": {EN: "Unreachable over SSH ({reason}).", RU: "Недоступен по SSH ({reason})."},
	"notify.test.server_degraded":  {EN: "{subject} is degraded", RU: "{subject} работает с перебоями"},
	"notify.auth.signed_in":        {EN: "Signed in from a new IP {ip}", RU: "Вход с нового адреса {ip}"},
	"notify.auth.locked":           {EN: "{failures} failed sign-ins from {ip}", RU: "Неудачных входов с {ip}: {failures}"},
	"notify.auth.password_changed": {EN: "The admin password was changed", RU: "Пароль администратора изменён"},
}

// Options of an env.
type Options struct {
	NoDispatcher bool // the test calls the subscriber itself
	NoJobs       bool // the test starts the job pools with StartJobs
	NotSetUp     bool // no token and chat saved
}

type env struct {
	t     *testing.T
	h     *jobstest.Harness
	svc   *notify.Service
	auth  *auth.Service
	tg    *telegram.Fake
	ch    *telegram.Channel
	clk   *clock
	names *names
}

func newEnv(t *testing.T, o Options) *env {
	t.Helper()
	h := jobstest.New(t)
	e := &env{t: t, h: h, clk: &clock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}, names: &names{by: map[string]string{"12": "nl-2"}}}
	must(t, h.Events.Declare(append(append([]events.Type(nil), testTypes...), append(auth.Events, notify.FailedEvent)...)...))
	must(t, h.Settings.Register(telegram.Section))
	cat := i18n.NewCatalog()
	must(t, cat.Add("test", testMessages))

	e.auth = auth.New(h.DB, h.Vault, h.Events, h.Settings)
	e.auth.Params = auth.Params{Memory: 1024, Time: 1, Threads: 1}
	must(t, e.auth.CreateAdmin(bg, "admin", "correct horse battery"))

	base, _ := url.Parse("https://proxier.test")
	e.svc = notify.New(h.DB, h.Events, cat, h.Sys, e.auth, h.Settings, base, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.svc.Now = h.Clock.Now
	must(t, e.svc.AddNamer(e.names))
	must(t, h.Sys.Register("platform", e.svc.JobType()))

	e.tg = telegram.NewFake(t, token)
	e.tg.Now = e.clk.Now
	e.ch = telegram.NewChannel(h.Settings, nil, e.tg.URL())
	e.ch.Now, e.ch.Sleep = e.clk.Now, e.clk.sleep
	must(t, e.svc.AddChannel(e.ch))
	if !o.NotSetUp {
		e.configure()
	}

	if !o.NoDispatcher {
		e.startDispatcher()
	}
	if !o.NoJobs {
		h.Start(h.Sys)
	}
	return e
}

// startDispatcher runs the event dispatcher with the notifier subscribed.
func (e *env) startDispatcher() {
	e.t.Helper()
	t, h := e.t, e.h
	d := events.NewDispatcher(h.DB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.Poll = func() time.Duration { return 5 * time.Millisecond }
	must(t, d.Subscribe(e.svc.Subscriber()))
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	// The subscriber's cursor starts at the newest event once the dispatcher
	// is up; nothing recorded before that is delivered.
	h.WaitFor("the notifier's cursor", func() bool {
		var n int
		_ = h.DB.R.Get(&n, `SELECT count(*) FROM event_cursors WHERE subscriber = ?`, notify.SubscriberName)
		return n == 1
	})
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) configure() {
	e.t.Helper()
	must(e.t, e.h.Settings.Set(bg, "admin", "telegram", map[string]string{
		"telegram.bot_token": token, "telegram.bot_username": "proxier_test_bot", "telegram.chat_id": chat, "telegram.chat_title": "tikhon",
	}))
}

// record writes an event, as a state change would, and returns its id.
func (e *env) record(typ string, subject events.Subject, payload map[string]any) int64 {
	e.t.Helper()
	var id int64
	must(e.t, e.h.DB.Write(bg, func(tx *sqlx.Tx) (err error) {
		id, err = e.h.Events.Record(bg, tx, events.Event{Type: typ, Subject: subject, Actor: events.ActorSystem, Payload: payload})
		return err
	}))
	return id
}

func (e *env) down(id string) int64 {
	return e.record("test.server_down", events.Subject{Type: "server", ID: id}, map[string]any{"reason": "timeout"})
}

// settle waits until the notifier has seen every recorded event.
func (e *env) settle() {
	e.t.Helper()
	e.h.WaitFor("the notifier to catch up", func() bool {
		var behind int
		err := e.h.DB.R.Get(&behind, `
			SELECT (SELECT COALESCE(MAX(id), 0) FROM events) - last_event_id FROM event_cursors WHERE subscriber = ?`, notify.SubscriberName)
		return err == nil && behind == 0
	})
}

func (e *env) sent(n int) []telegram.Sent {
	e.t.Helper()
	e.h.WaitFor("messages to be sent", func() bool { return len(e.tg.Sent()) >= n })
	return e.tg.Sent()
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	must(e.t, e.h.DB.R.Get(&n, q, args...))
	return n
}

package notify_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
)

func TestNotifyingEventSendsOneMessageWithButton(t *testing.T) {
	e := newEnv(t, Options{})
	e.down("12")
	e.settle()
	e.h.Drain()
	got := e.sent(1)
	if len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
	m := got[0]
	want := "🔴 <b>nl-2 is down</b>\nUnreachable over SSH (timeout)."
	if m.Text != want || m.ChatID != chat || m.ParseMode != "HTML" || !m.PreviewsOff {
		t.Fatalf("%+v", m)
	}
	if m.ButtonText != "Open in Proxier" || m.ButtonURL != "https://proxier.test/servers/12" {
		t.Fatalf("button %q → %q", m.ButtonText, m.ButtonURL)
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE state = 'sent' AND attempts = 1`); n != 1 {
		t.Fatalf("%d sent notifications", n)
	}
}

func TestDuplicateDeliveryNotifiesOnce(t *testing.T) {
	e := newEnv(t, Options{NoDispatcher: true})
	id := e.down("12")
	evs, err := events.After(bg, e.h.DB.R, id-1, 10)
	must(t, err)
	// The restart between delivery and the cursor update: the same event twice.
	for range 2 {
		must(t, e.h.DB.Write(bg, func(tx *sqlx.Tx) error { return e.svc.Subscriber().Handle(bg, tx, evs[0]) }))
	}
	e.h.Drain()
	if got := e.sent(1); len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
	if n := e.count(`SELECT count(*) FROM notifications`); n != 1 {
		t.Fatalf("%d notifications", n)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE type = 'platform.notify'`); n != 1 {
		t.Fatalf("%d delivery jobs", n)
	}
}

func TestRuleOffSendsNothing(t *testing.T) {
	e := newEnv(t, Options{})
	must(t, e.svc.SetRule(bg, "test.server_down", false))
	id := e.down("12")
	e.settle()
	e.h.Drain()
	if n := len(e.tg.Sent()); n != 0 {
		t.Fatalf("%d messages with the rule off", n)
	}
	// Still in Activity.
	list, err := events.List(bg, e.h.DB.R, events.Filter{Type: "test.server_down", Limit: 10})
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("%v %v", list, err)
	}
	// A rule that is off by default stays off.
	e.record("test.server_degraded", events.Subject{Type: "server", ID: "12"}, nil)
	e.settle()
	e.h.Drain()
	if n := len(e.tg.Sent()); n != 0 {
		t.Fatalf("%d messages for a degraded server", n)
	}
}

func TestMessageInAdminLanguage(t *testing.T) {
	e := newEnv(t, Options{})
	must(t, e.auth.SetLanguage(bg, i18n.RU))
	e.down("12")
	e.settle()
	got := e.sent(1)
	if got[0].Text != "🔴 <b>nl-2 недоступен</b>\nНедоступен по SSH (timeout)." {
		t.Fatalf("%q", got[0].Text)
	}
	if got[0].ButtonText != "Открыть в Proxier" {
		t.Fatalf("%q", got[0].ButtonText)
	}
}

func TestTextIsRenderedWhenQueued(t *testing.T) {
	e := newEnv(t, Options{NoJobs: true})
	e.down("12")
	e.settle() // queued, not yet delivered
	e.names.rename("12", "nl-2-renamed")
	e.h.Start(e.h.Sys)
	got := e.sent(1)
	if !strings.Contains(got[0].Text, "nl-2 is down") || strings.Contains(got[0].Text, "renamed") {
		t.Fatalf("%q", got[0].Text)
	}
	if !strings.HasSuffix(got[0].ButtonURL, "/servers/12") {
		t.Fatalf("%q", got[0].ButtonURL)
	}
}

func TestSignedInNotifiesOnlyFromNewIP(t *testing.T) {
	e := newEnv(t, Options{})
	admin := events.Subject{Type: "admin", ID: "1"}
	e.record("auth.signed_in", admin, map[string]any{"ip": "198.51.100.4", "user_agent": "x", "new_ip": false})
	e.settle()
	e.h.Drain()
	if n := len(e.tg.Sent()); n != 0 {
		t.Fatalf("%d messages for a known IP", n)
	}
	e.record("auth.signed_in", admin, map[string]any{"ip": "203.0.113.7", "user_agent": "x", "new_ip": true})
	e.settle()
	got := e.sent(1)
	if len(got) != 1 || !strings.Contains(got[0].Text, "203.0.113.7") || !strings.HasPrefix(got[0].Text, "🔐") {
		t.Fatalf("%+v", got)
	}
	if got[0].ButtonURL != "https://proxier.test/settings/security" {
		t.Fatalf("admin page link %q", got[0].ButtonURL)
	}
}

func TestUnconfiguredChannelQueuesNothing(t *testing.T) {
	e := newEnv(t, Options{NotSetUp: true})
	if ok, err := e.svc.Configured(bg); err != nil || ok {
		t.Fatalf("configured: %v %v", ok, err)
	}
	e.down("12")
	e.settle()
	e.h.Drain()
	if n := e.count(`SELECT count(*) FROM notifications`); n != 0 {
		t.Fatalf("%d notifications while unconfigured", n)
	}
	// Enabling it later sends no backlog.
	e.configure()
	e.settle()
	e.h.Drain()
	if n := len(e.tg.Sent()); n != 0 {
		t.Fatalf("%d old events were sent", n)
	}
	e.down("12")
	e.settle()
	if got := e.sent(1); len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
}

type customRenderer struct{}

func (customRenderer) RenderNotification(_ context.Context, ev events.Event, loc *i18n.Localizer) (notify.Message, bool, error) {
	if ev.Type != "test.custom" {
		return notify.Message{}, false, nil
	}
	return notify.Message{Title: "Custom <" + string(loc.Lang) + ">", Body: "by the module"}, true, nil
}

func TestModuleRendererTakesOver(t *testing.T) {
	e := newEnv(t, Options{NoDispatcher: true})
	must(t, e.svc.AddRenderer("test", customRenderer{}))
	// The dispatcher is started by hand here so the renderer is registered first.
	e.startDispatcher()
	e.record("test.custom", events.Subject{Type: "server", ID: "12"}, nil)
	e.down("12") // not the renderer's type: the default text
	e.settle()
	got := e.sent(2)
	if got[0].Text != "🧭 <b>Custom &lt;en&gt;</b>\nby the module" {
		t.Fatalf("%q", got[0].Text)
	}
	// The emoji, the link and the button are filled in for the renderer.
	if got[0].ButtonText != "Open in Proxier" || got[0].ButtonURL != "https://proxier.test/servers/12" {
		t.Fatalf("%q %q", got[0].ButtonText, got[0].ButtonURL)
	}
	if !strings.Contains(got[1].Text, "nl-2 is down") {
		t.Fatalf("default text: %q", got[1].Text)
	}
}

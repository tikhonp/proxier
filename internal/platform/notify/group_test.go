package notify_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/notify"
)

func TestBurstOfOneKindIsOneMessage(t *testing.T) {
	e := newEnv(t, Options{})
	e.names.rename("13", "de-1")
	e.names.rename("14", "fi-1")
	e.health("12", "blocked")
	e.health("13", "blocked")
	e.health("14", "down") // another kind: its own group
	e.settle()
	e.h.Drain()
	if n := len(e.tg.Sent()); n != 0 {
		t.Fatalf("%d sent inside the window", n)
	}
	e.health("14", "blocked") // still inside the window
	e.settle()

	e.h.Clock.Advance(notify.GroupWindow)
	e.h.Drain()
	got := e.sent(2)
	if len(got) != 2 {
		t.Fatalf("%d messages: %+v", len(got), got)
	}
	var group, lone string
	for _, m := range got {
		if strings.Contains(m.Text, "servers are blocked") {
			group = m.Text
		} else {
			lone = m.Text
		}
	}
	want := "🟣 <b>3 servers are blocked</b>\nnl-2 is blocked: Because tcp.\nde-1 is blocked: Because tcp.\nfi-1 is blocked: Because tcp."
	if group != want {
		t.Fatalf("group:\n%s\nwant:\n%s", group, want)
	}
	if lone != "🟣 <b>fi-1 is down</b>\nBecause tcp." {
		t.Fatalf("a lone change is sent as it is: %q", lone)
	}
	for _, m := range got {
		if strings.Contains(m.Text, "servers are blocked") && m.ButtonURL != "https://proxier.test/servers" {
			t.Fatalf("group button %q", m.ButtonURL)
		}
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE state = 'sent' AND attempts = 1`); n != 4 {
		t.Fatalf("%d sent notifications", n)
	}

	// After the window a new change starts a new group.
	e.health("12", "blocked")
	e.settle()
	e.h.Clock.Advance(notify.GroupWindow)
	e.h.Drain()
	if got := e.sent(3); !strings.HasPrefix(got[2].Text, "🟣 <b>nl-2 is blocked</b>") {
		t.Fatalf("%q", got[2].Text)
	}
}

func TestFailedGroupIsRetriedWhole(t *testing.T) {
	e := newEnv(t, Options{})
	e.names.rename("13", "de-1")
	e.tg.SetDown(true)
	e.health("12", "blocked")
	e.health("13", "blocked")
	e.settle()
	e.h.Clock.Advance(notify.GroupWindow)
	for range 4 { // through every backoff
		e.h.Drain()
		e.h.Clock.Advance(notify.GroupWindow * 5)
	}
	e.h.Drain()
	if n := e.count(`SELECT count(*) FROM notifications WHERE state = 'failed' AND attempts = 4`); n != 2 {
		t.Fatalf("%d failed notifications", n)
	}
	failed, err := e.svc.Failed(bg, e.h.Clock.Now().Add(-notify.GroupWindow*100), 10)
	if err != nil || len(failed) != 2 {
		t.Fatalf("%+v %v", failed, err)
	}
	e.tg.SetDown(false)
	must(t, e.svc.Retry(bg, failed[1].ID))
	got := e.sent(1)
	if len(got) != 1 || !strings.HasPrefix(got[0].Text, "🟣 <b>2 servers are blocked</b>") {
		t.Fatalf("%+v", got)
	}
	e.h.Drain()
	if n := e.count(`SELECT count(*) FROM notifications WHERE state = 'sent'`); n != 2 {
		t.Fatalf("%d sent after the retry", n)
	}
}

package notify_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
)

func TestBurstIsPacedInOrder(t *testing.T) {
	e := newEnv(t, Options{})
	const n = 30
	for i := range n {
		e.names.rename(string(rune('a'+i)), "srv-"+string(rune('a'+i)))
		e.down(string(rune('a' + i)))
	}
	got := e.sent(n)
	if len(got) != n {
		t.Fatalf("%d messages", len(got))
	}
	for i, m := range got {
		want := "srv-" + string(rune('a'+i))
		if i > 0 && m.At.Sub(got[i-1].At) < time.Second {
			t.Fatalf("message %d came %s after the one before", i, m.At.Sub(got[i-1].At))
		}
		if want != "" && !strings.Contains(m.Text, want+" is down") {
			t.Fatalf("message %d is %q, want %s: out of order", i, m.Text, want)
		}
	}
}

func (e *env) jobAttempt() (jobs.State, int, time.Time) {
	e.t.Helper()
	var r struct {
		State    string `db:"state"`
		Attempt  int    `db:"attempt"`
		RunAfter string `db:"run_after"`
	}
	if err := e.h.DB.R.Get(&r, `SELECT state, attempt, run_after FROM jobs WHERE type = 'platform.notify' ORDER BY id LIMIT 1`); err != nil {
		return "", 0, time.Time{} // not queued yet
	}
	at, _ := time.Parse("2006-01-02T15:04:05.000Z", r.RunAfter)
	return jobs.State(r.State), r.Attempt, at
}

func TestRateLimitWaitsWithoutCountingAttempt(t *testing.T) {
	e := newEnv(t, Options{})
	e.tg.FailNext(telegram.Failure{Code: 429, Description: "Too Many Requests: retry after 20", RetryAfter: 20})
	e.down("12")
	e.h.WaitFor("the message to be deferred", func() bool {
		st, _, _ := e.jobAttempt()
		return st == jobs.Queued && len(e.tg.Calls()) > 0
	})
	_, _, runAfter := e.jobAttempt()
	if wait := runAfter.Sub(e.h.Clock.Now()); wait != 20*time.Second {
		t.Fatalf("waits %s, want 20s", wait)
	}
	if n := len(e.tg.Sent()); n != 0 {
		t.Fatalf("sent %d before the wait", n)
	}
	e.h.Clock.Advance(20 * time.Second)
	if got := e.sent(1); len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
	e.h.Drain()
	// The wait was not a try: the job ran once, and one try is counted.
	if _, attempt, _ := e.jobAttempt(); attempt != 1 {
		t.Fatalf("job attempt %d, want 1", attempt)
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE state = 'sent' AND attempts = 1`); n != 1 {
		t.Fatalf("attempts not counted as one")
	}
}

// failFor runs the retries of the first notification out: 10 s, 1 min, 5 min.
func (e *env) exhaust() {
	e.t.Helper()
	for range 4 {
		e.h.Drain()
		e.h.Clock.Advance(6 * time.Minute)
	}
	e.h.Drain()
}

func TestOutageFailsAfterRetriesAndLaterMessagesTry(t *testing.T) {
	e := newEnv(t, Options{})
	e.tg.SetDown(true)
	first := e.down("12")
	e.settle()
	e.exhaust()
	var n struct {
		State    string `db:"state"`
		Attempts int    `db:"attempts"`
		Err      string `db:"last_error"`
	}
	must(t, e.h.DB.R.Get(&n, `SELECT state, attempts, last_error FROM notifications WHERE event_id = ?`, first))
	if n.State != "failed" || n.Attempts != 4 || n.Err == "" {
		t.Fatalf("%+v", n)
	}
	failed, err := e.svc.Failed(bg, e.h.Clock.Now().Add(-time.Hour), 10)
	if err != nil || len(failed) != 1 || failed[0].EventID != first {
		t.Fatalf("failed list: %+v %v", failed, err)
	}
	if bad, _ := e.svc.LatestFailed(bg); !bad {
		t.Fatal("the header should warn")
	}

	// Telegram is back: later messages still try, and clear the warning.
	e.tg.SetDown(false)
	e.names.rename("13", "nl-3")
	e.down("13")
	if got := e.sent(1); len(got) != 1 || !strings.Contains(got[0].Text, "nl-3") {
		t.Fatalf("%+v", got)
	}
	e.h.Drain()
	if bad, _ := e.svc.LatestFailed(bg); bad {
		t.Fatal("the warning outlived a delivered message")
	}

	// Retry resends the failed one.
	if err := e.svc.Retry(bg, failed[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := e.sent(2); len(got) != 2 || !strings.Contains(got[1].Text, "nl-2 is down") {
		t.Fatalf("%+v", got)
	}
	e.h.Drain()
	if n := e.count(`SELECT count(*) FROM notifications WHERE state = 'sent'`); n != 2 {
		t.Fatalf("%d sent after the retry", n)
	}
}

func TestDeliveryFailureNeverNotifies(t *testing.T) {
	e := newEnv(t, Options{})
	e.tg.SetDown(true)
	first := e.down("12")
	e.settle()
	e.exhaust()
	e.settle() // the notifier has seen notification.failed
	e.h.Drain()
	evs, err := events.List(bg, e.h.DB.R, events.Filter{Type: "notification.failed", Limit: 10})
	if err != nil || len(evs) != 1 {
		t.Fatalf("notification.failed: %v %v", evs, err)
	}
	if s := evs[0].Subject; s.Type != "event" || s.ID != strconv.FormatInt(first, 10) {
		t.Fatalf("subject %v", s)
	}
	if evs[0].Payload["channel"] != "telegram" {
		t.Fatalf("payload %v", evs[0].Payload)
	}
	if n := e.count(`SELECT count(*) FROM events WHERE type = 'job.failed'`); n != 0 {
		t.Fatalf("%d job.failed events for a failed notification", n)
	}
	// Only the first event ever became a notification.
	if n := e.count(`SELECT count(*) FROM notifications`); n != 1 {
		t.Fatalf("%d notifications, a failure was notified about", n)
	}
}

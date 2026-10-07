package platform_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// TestFailedDemoJobReachesTelegram is the Phase 0 exit demo's last leg: a job
// fails, its event reaches the notifier, and the bot gets it in the admin's
// language with a button to the job.
func TestFailedDemoJobReachesTelegram(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	app := s.App
	f := telegram.NewFake(t, "777:E2E-token")
	app.Telegram.BaseURL = f.URL()
	if err := app.Settings.Set(ctx, "admin", "telegram", map[string]string{
		"telegram.bot_token": "777:E2E-token", "telegram.chat_id": "42",
	}); err != nil {
		t.Fatal(err)
	}
	app.Jobs.Poll = 5 * time.Millisecond
	app.Dispatcher.Poll = func() time.Duration { return 5 * time.Millisecond }

	run, stop := context.WithCancel(ctx)
	done := make(chan struct{}, 2)
	go func() { _ = app.Jobs.Start(run); done <- struct{}{} }()
	go func() { _ = app.Dispatcher.Start(run); done <- struct{}{} }()
	t.Cleanup(func() { stop(); <-done; <-done })

	// The dispatcher's cursors start at the newest event once it is up.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		_ = app.DB.R.Get(&n, `SELECT count(*) FROM event_cursors WHERE subscriber = 'platform.notifier'`)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the notifier never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	e, err := app.Jobs.EnqueueNow(ctx, jobs.Request{
		Type: "platform.demo", Payload: jobs.DemoPayload{Seconds: 1, IntervalMS: 1, Fail: true}, CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	for len(f.Sent()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("nothing reached Telegram")
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := f.Sent()[0]
	if got.ChatID != "42" || !strings.HasPrefix(got.Text, "🔴 <b>Job #") || !strings.Contains(got.Text, "failed") ||
		!strings.Contains(got.Text, "demo failure, as asked") {
		t.Fatalf("%q", got.Text)
	}
	if got.ButtonText != "Open in Proxier" || got.ButtonURL != "http://proxier.test/jobs/"+strconv.FormatInt(e.ID, 10) {
		t.Fatalf("%q %q", got.ButtonText, got.ButtonURL)
	}
	// Only the failure was sent: the settings save and the delivery itself are not notifying.
	time.Sleep(50 * time.Millisecond)
	if n := len(f.Sent()); n != 1 {
		t.Fatalf("%d messages", n)
	}
}

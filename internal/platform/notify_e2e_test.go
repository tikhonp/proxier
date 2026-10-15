package platform_test

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"github.com/tikhonp/proxier/internal/platform/testwait"
)

// TestFailedDemoJobReachesTelegram follows a failure end to end: a job
// fails, its event reaches the notifier, and the bot gets it in member 1's
// chat and language with a button to the job.
func TestFailedDemoJobReachesTelegram(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	app := s.App
	f := telegram.NewFake(t, "777:E2E-token")
	app.Telegram.BaseURL = f.URL()
	if err := app.Settings.Set(ctx, "admin", "telegram", map[string]string{"telegram.bot_token": "777:E2E-token"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Auth.SetTelegramChat(ctx, 1, "42", "Tikhon"); err != nil {
		t.Fatal(err)
	}
	deadline := run(t, s)

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

// run starts the app's jobs and dispatcher until the test ends, once the
// notifier's cursor is up, and returns the test's deadline.
func run(t *testing.T, s *sitetest.Site) time.Time {
	t.Helper()
	app := s.App
	app.Jobs.Poll = 5 * time.Millisecond
	app.Dispatcher.Poll = func() time.Duration { return 5 * time.Millisecond }

	run, stop := context.WithCancel(ctx)
	done := make(chan struct{}, 2)
	go func() { _ = app.Jobs.Start(run); done <- struct{}{} }()
	go func() { _ = app.Dispatcher.Start(run); done <- struct{}{} }()
	t.Cleanup(func() { stop(); <-done; <-done })

	// The dispatcher's cursors start at the newest event once it is up.
	deadline := testwait.Deadline(30 * time.Second)
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

	return deadline
}

// thingsModule owns things in the subscriptions area: thing:5 is member 2's.
type thingsModule struct{}

func (thingsModule) Name() string      { return "subscriptions" }
func (thingsModule) Migrations() fs.FS { return nil }
func (thingsModule) EventTypes() []events.Type {
	return []events.Type{{Name: "thing.flagged", Module: "subscriptions", Notify: true, Emoji: "🔗", Description: "A thing looks shared."}}
}
func (thingsModule) Messages() i18n.Messages {
	return i18n.Messages{
		"event.thing.flagged":  {EN: "Flagged {subject}", RU: "Отмечено: {subject}"},
		"notify.thing.flagged": {EN: "{subject} looks shared", RU: "{subject}: похоже, этим делятся"},
	}
}
func (thingsModule) OwnedSubjects(context.Context, int64) ([]events.Subject, error) { return nil, nil }
func (thingsModule) Owner(_ context.Context, s events.Subject) (int64, bool, error) {
	return 2, s.String() == "thing:5", nil
}

// A member's thing reaches their own chat, in their language, and the chat
// of who manages all of the area, and nobody else's: the module's owner of
// the subject is asked through the platform.
func TestNotificationReachesTheOwnerAndManageAll(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{thingsModule{}}})
	app := s.App
	f := telegram.NewFake(t, "777:E2E-token")
	app.Telegram.BaseURL = f.URL()
	if err := app.Settings.Set(ctx, "admin", "telegram", map[string]string{"telegram.bot_token": "777:E2E-token"}); err != nil {
		t.Fatal(err)
	}
	anna := s.Join("anna", access.Grant{}.With(access.Subscriptions, access.LevelOwn), "")
	bob := s.Join("bob", access.Grant{}.With(access.Subscriptions, access.LevelViewAll), "")
	for id, chat := range map[int64]string{1: "41", anna.Member: "42", bob.Member: "43"} {
		if err := app.Auth.SetTelegramChat(ctx, id, chat, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Auth.SetLanguage(ctx, anna.Member, i18n.RU); err != nil {
		t.Fatal(err)
	}
	deadline := run(t, s)
	if err := app.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := app.Events.Record(ctx, tx, events.Event{Type: "thing.flagged", Subject: events.Subject{Type: "thing", ID: "5"}, Actor: events.ActorSystem})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for len(f.Sent()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("sent %+v", f.Sent())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	got := map[string]string{}
	for _, m := range f.Sent() {
		got[m.ChatID] = m.Text
	}
	if len(got) != 2 || got["41"] != "🔗 <b>thing:5 looks shared</b>" || got["42"] != "🔗 <b>thing:5: похоже, этим делятся</b>" {
		t.Fatalf("sent %v", got)
	}
}

package pages_test

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

func TestDashboardShowsNotificationHealth(t *testing.T) {
	s, l, _ := telegramSite(t)
	page := l.Get("/").Body.String()
	if !strings.Contains(page, "Notifications are not configured") || !strings.Contains(page, "/settings/integrations/telegram") {
		t.Fatalf("the band is missing:\n%s", page)
	}
	if strings.Contains(page, "Failed notifications") || strings.Contains(page, "Telegram failing") {
		t.Error("nothing failed yet")
	}

	saveToken(t, l)
	page = l.Get("/").Body.String()
	if strings.Contains(page, "not configured") || !strings.Contains(page, "Your notifications have nowhere to go") {
		t.Error("with a bot, the band should ask for the member's chat instead")
	}
	if rec := l.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"5"}}); rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	if page := l.Get("/").Body.String(); strings.Contains(page, "nowhere to go") {
		t.Error("the band stays after the chat was chosen")
	}

	// A failed delivery: listed with its error and Retry, and the header
	// warns its own recipient only.
	anna := s.Join("anna", access.PresetOperator.Grant(), "198.51.100.2:1")
	now := db.Now()
	_, err := s.App.DB.W.Exec(`
		INSERT INTO notifications (event_id, channel, member_id, lang, message, state, attempts, last_error, created_at)
		VALUES (7, 'telegram', 1, 'en', '{"emoji":"🔴","title":"nl-2 is down","body":"","url":"","button":""}', 'failed', 4, 'telegram: 502 Bad Gateway', ?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	page = l.Get("/").Body.String()
	for _, want := range []string{"Failed notifications", "nl-2 is down", "telegram: 502 Bad Gateway", "/notifications/1/retry", "Telegram failing", `id="failed-notifications"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the dashboard lacks %q", want)
		}
	}
	if page := anna.Get("/").Body.String(); strings.Contains(page, "Failed notifications") || strings.Contains(page, "Telegram failing") {
		t.Error("anna is shown member 1's failed notification")
	}
	if rec := anna.Post("/notifications/1/retry", nil); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	var state string
	if err := s.App.DB.R.Get(&state, `SELECT state FROM notifications WHERE id = 1`); err != nil || state != "failed" {
		t.Fatalf("anna retried member 1's notification: %q %v", state, err)
	}
	// Failures older than a week drop off the list.
	_, err = s.App.DB.W.Exec(`UPDATE notifications SET created_at = ?`, db.At(time.Now().Add(-8*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if page := l.Get("/").Body.String(); strings.Contains(page, "Failed notifications") {
		t.Error("a failure from 8 days ago is still listed")
	}
}

// areaModule is a module that adds areas to the dashboard.
type areaModule struct{ fail bool }

func (areaModule) Name() string      { return "areas" }
func (areaModule) Migrations() fs.FS { return nil }
func (m areaModule) Dashboard(context.Context, access.Who) ([]ui.DashboardArea, error) {
	if m.fail {
		return nil, errors.New("the query failed")
	}
	return []ui.DashboardArea{
		{Area: access.Servers, Order: 20, Title: "Second area", Body: templ.Raw("<p>second body</p>")},
		{Area: access.Routing, Order: 10, Title: "First area", Body: templ.Raw("<p>first body</p>")},
	}, nil
}

func TestDashboardShowsModuleAreasInOrder(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{areaModule{}}})
	page := s.SignIn("").Get("/").Body.String()
	first, second := strings.Index(page, "First area"), strings.Index(page, "Second area")
	if first < 0 || second < first || !strings.Contains(page, "first body") || !strings.Contains(page, "second body") {
		t.Fatalf("areas missing or out of order:\n%s", page)
	}
	if strings.Contains(page, "Nothing needs attention.") {
		t.Error("the quiet message stays beside an area")
	}

	// A failing module does not take the dashboard down.
	s = sitetest.New(t, sitetest.Options{Modules: []module.Module{areaModule{fail: true}}})
	if rec := s.SignIn("").Get("/"); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

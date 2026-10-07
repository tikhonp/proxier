package pages_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/db"
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
	if rec := l.Post("/settings/integrations/telegram/chat", url.Values{"chat_id": {"5"}}); rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	page = l.Get("/").Body.String()
	if strings.Contains(page, "not configured") {
		t.Error("the band stays after setup")
	}

	// A failed delivery: listed with its error and Retry, and the header warns.
	now := db.Now()
	_, err := s.App.DB.W.Exec(`
		INSERT INTO notifications (event_id, channel, lang, message, state, attempts, last_error, created_at)
		VALUES (7, 'telegram', 'en', '{"emoji":"🔴","title":"nl-2 is down","body":"","url":"","button":""}', 'failed', 4, 'telegram: 502 Bad Gateway', ?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	page = l.Get("/").Body.String()
	for _, want := range []string{"Failed notifications", "nl-2 is down", "telegram: 502 Bad Gateway", "/notifications/1/retry", "Telegram failing", `id="failed-notifications"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the dashboard lacks %q", want)
		}
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

package pages_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

const tgToken = "123456:PAGE-token"

// telegramSite is a signed-in site whose Telegram is the fake bot.
func telegramSite(t *testing.T) (*sitetest.Site, *sitetest.Login, *telegram.Fake) {
	t.Helper()
	s := sitetest.New(t, sitetest.Options{})
	f := telegram.NewFake(t, tgToken)
	s.App.Telegram.BaseURL = f.URL()
	return s, s.SignIn(""), f
}

func saveToken(t *testing.T, l *sitetest.Login) {
	t.Helper()
	if rec := l.Post("/settings/integrations/telegram/token", url.Values{"token": {tgToken}}); rec.Code != 303 {
		t.Fatalf("saving the token: %d\n%s", rec.Code, rec.Body)
	}
}

func TestInvalidBotTokenSavesNothing(t *testing.T) {
	s, l, f := telegramSite(t)
	rec := l.Post("/settings/integrations/telegram/token", url.Values{"token": {"654321:WRONG-token"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Telegram refused this token.") {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "WRONG-token") {
		t.Error("the refused token was echoed")
	}
	for _, key := range []string{"telegram.bot_token", "telegram.bot_username"} {
		if got, _ := s.App.Settings.Get(t.Context(), key); got != "" {
			t.Errorf("%s saved as %q", key, got)
		}
	}
	if n := len(f.Calls()); n != 1 {
		t.Errorf("%d calls, want only getMe", n)
	}
	// A good token saves with the bot's name.
	saveToken(t, l)
	if got, _ := s.App.Settings.Get(t.Context(), "telegram.bot_username"); got != "proxier_test_bot" {
		t.Errorf("username %q", got)
	}
	if page := l.Get("/settings/integrations/telegram").Body.String(); !strings.Contains(page, "@proxier_test_bot") || strings.Contains(page, "PAGE-token") {
		t.Errorf("the page should name the bot and never show the token:\n%s", page)
	}
}

func TestDetectChatWithoutUpdates(t *testing.T) {
	_, l, f := telegramSite(t)
	if body := l.Post("/settings/integrations/telegram/detect", nil).Body.String(); !strings.Contains(body, "Save a bot token first.") {
		t.Errorf("before any token:\n%s", body)
	}
	saveToken(t, l)
	body := l.Post("/settings/integrations/telegram/detect", nil).Body.String()
	if !strings.Contains(body, "No chats yet. Send /start to @proxier_test_bot and try again.") {
		t.Fatalf("%s", body)
	}

	// After /start the chat is offered, and choosing it saves it.
	f.AddUpdate(`{"update_id":1,"message":{"chat":{"id":5,"type":"private","first_name":"Tikhon"}}}`)
	body = l.Post("/settings/integrations/telegram/detect", nil).Body.String()
	if !strings.Contains(body, "Tikhon") || !strings.Contains(body, `name="chat_id" value="5"`) {
		t.Fatalf("%s", body)
	}
	if rec := l.Post("/settings/integrations/telegram/chat", url.Values{"chat_id": {"5"}, "chat_title": {"Tikhon"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestSendTestErrorKeepsChat(t *testing.T) {
	s, l, f := telegramSite(t)
	saveToken(t, l)
	f.Block("5")
	if rec := l.Post("/settings/integrations/telegram/chat", url.Values{"chat_id": {"5"}, "chat_title": {"Tikhon"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	body := l.Post("/settings/integrations/telegram/test", nil).Body.String()
	if !strings.Contains(body, "bot was blocked by the user") || strings.Contains(body, "Sent.") {
		t.Fatalf("the error should show:\n%s", body)
	}
	if got, _ := s.App.Settings.Get(t.Context(), "telegram.chat_id"); got != "5" {
		t.Errorf("chat %q, want it saved anyway", got)
	}
	if strings.Contains(body, tgToken) {
		t.Error("the token is in the error")
	}

	// A chat that works: the test arrives, with the button.
	if rec := l.Post("/settings/integrations/telegram/chat", url.Values{"chat_id": {"6"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	if body := l.Post("/settings/integrations/telegram/test", nil).Body.String(); !strings.Contains(body, "Sent. Check the chat.") {
		t.Fatalf("%s", body)
	}
	got := f.Sent()
	if len(got) != 1 || got[0].ChatID != "6" || got[0].ButtonURL != "http://proxier.test/settings/integrations/telegram" {
		t.Fatalf("%+v", got)
	}
}

func TestInvalidChatIDIsRefused(t *testing.T) {
	s, l, _ := telegramSite(t)
	rec := l.Post("/settings/integrations/telegram/chat", url.Values{"chat_id": {"not a chat"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Must be a number, or @channel.") {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if got, _ := s.App.Settings.Get(t.Context(), "telegram.chat_id"); got != "" {
		t.Errorf("saved %q", got)
	}
}

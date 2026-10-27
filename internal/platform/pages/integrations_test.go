package pages_test

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/access"
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

// poll has the bot read what the fake holds, as its loop would.
func poll(t *testing.T, s *sitetest.Site) {
	t.Helper()
	if err := s.App.Bot.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
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

func chatOf(t *testing.T, s *sitetest.Site, id int64) string {
	t.Helper()
	m, err := s.App.Auth.Member(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m.TelegramChatID
}

func TestDetectChatWithoutUpdates(t *testing.T) {
	s, l, f := telegramSite(t)
	if body := l.Post("/settings/notifications/telegram/detect", nil).Body.String(); !strings.Contains(body, "Save a bot token first.") {
		t.Errorf("before any token:\n%s", body)
	}
	saveToken(t, l)
	body := l.Post("/settings/notifications/telegram/detect", nil).Body.String()
	if !strings.Contains(body, "No chats yet. Send /start to @proxier_test_bot and try again.") {
		t.Fatalf("%s", body)
	}

	// After /start the chat is offered, and choosing it makes it the member's.
	f.AddUpdate(`{"update_id":1,"message":{"chat":{"id":5,"type":"private","first_name":"Tikhon"}}}`)
	poll(t, s)
	body = l.Post("/settings/notifications/telegram/detect", nil).Body.String()
	if !strings.Contains(body, "Tikhon") || !strings.Contains(body, `name="chat_id" value="5"`) || !strings.Contains(body, `action="/settings/notifications/telegram/chat"`) {
		t.Fatalf("%s", body)
	}
	if rec := l.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"5"}, "chat_title": {"Tikhon"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	if got := chatOf(t, s, 1); got != "5" {
		t.Fatalf("member 1's chat %q", got)
	}
	// templ drops a space that opens an element, so the one before "·" sits outside the span
	if page := l.Get("/settings/notifications?saved=chat").Body.String(); !strings.Contains(page, "Chat saved.") || !strings.Contains(page, `>Tikhon <span class="muted">· 5</span>`) {
		t.Errorf("the page doesn't show the saved chat:\n%s", page)
	}
}

// Detect chat offers no chat another member already has: it is theirs, and
// naming it tells who else gets notified where.
func TestDetectChatLeavesOutOtherMembersChats(t *testing.T) {
	s, l, f := telegramSite(t)
	saveToken(t, l)
	if rec := l.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"5"}, "chat_title": {"Tikhon"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	anna := s.Join("anna", access.PresetViewer.Grant(), "198.51.100.2:1")
	f.AddUpdate(`{"update_id":1,"message":{"chat":{"id":5,"type":"private","first_name":"Tikhon"}}}`)
	f.AddUpdate(`{"update_id":2,"message":{"chat":{"id":6,"type":"private","first_name":"Anna"}}}`)
	poll(t, s)
	body := anna.Post("/settings/notifications/telegram/detect", nil).Body.String()
	if strings.Contains(body, "Tikhon") || strings.Contains(body, `value="5"`) || !strings.Contains(body, "Anna") {
		t.Fatalf("anna is offered:\n%s", body)
	}
	// Member 1's own chat is still offered to member 1.
	if body := l.Post("/settings/notifications/telegram/detect", nil).Body.String(); !strings.Contains(body, "Tikhon") {
		t.Fatalf("member 1 isn't offered their own chat:\n%s", body)
	}
}

func TestSendTestErrorKeepsChat(t *testing.T) {
	s, l, f := telegramSite(t)
	saveToken(t, l)
	f.Block("5")
	if rec := l.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"5"}, "chat_title": {"Tikhon"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	body := l.Post("/settings/notifications/telegram/test", nil).Body.String()
	if !strings.Contains(body, "bot was blocked by the user") || strings.Contains(body, "Sent.") {
		t.Fatalf("the error should show:\n%s", body)
	}
	if got := chatOf(t, s, 1); got != "5" {
		t.Errorf("chat %q, want it saved anyway", got)
	}
	if strings.Contains(body, tgToken) {
		t.Error("the token is in the error")
	}

	// A chat that works: the test arrives, with the button.
	if rec := l.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"6"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	if body := l.Post("/settings/notifications/telegram/test", nil).Body.String(); !strings.Contains(body, "Sent. Check the chat.") {
		t.Fatalf("%s", body)
	}
	got := f.Sent()
	if len(got) != 1 || got[0].ChatID != "6" || got[0].ButtonURL != "http://proxier.test/settings/notifications" {
		t.Fatalf("%+v", got)
	}
}

// Every member chooses their own chat, whatever their grant, and Send test
// goes to theirs alone.
func TestEachMemberHasTheirOwnChat(t *testing.T) {
	s, l, f := telegramSite(t)
	saveToken(t, l)
	if rec := l.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"5"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	anna := s.Join("anna", access.Grant{}, "198.51.100.2:1")
	page := anna.Get("/settings/notifications").Body.String()
	if !strings.Contains(page, "No chat yet.") || strings.Contains(page, `value="5"`) {
		t.Fatalf("anna's page shows member 1's chat:\n%s", page)
	}
	if strings.Contains(page, "/settings/integrations/telegram") {
		t.Error("anna, who can't, is sent to set up the bot")
	}
	if !strings.Contains(anna.Get("/").Body.String(), "Your notifications have nowhere to go") {
		t.Error("anna's dashboard doesn't ask for her chat")
	}
	if rec := anna.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"7"}, "chat_title": {"Anna"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	if chatOf(t, s, 1) != "5" || chatOf(t, s, anna.Member) != "7" {
		t.Fatalf("chats %q and %q", chatOf(t, s, 1), chatOf(t, s, anna.Member))
	}
	if strings.Contains(anna.Get("/").Body.String(), "nowhere to go") {
		t.Error("the band stays after anna chose her chat")
	}
	if body := anna.Post("/settings/notifications/telegram/test", nil).Body.String(); !strings.Contains(body, "Sent.") {
		t.Fatalf("%s", body)
	}
	if got := f.Sent(); len(got) != 1 || got[0].ChatID != "7" {
		t.Fatalf("%+v", got)
	}
	// An empty chat stops her notifications.
	if rec := anna.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {""}}); rec.Code != 303 || chatOf(t, s, anna.Member) != "" {
		t.Fatalf("%d, chat %q", rec.Code, chatOf(t, s, anna.Member))
	}
}

func TestInvalidChatIDIsRefused(t *testing.T) {
	s, l, _ := telegramSite(t)
	rec := l.Post("/settings/notifications/telegram/chat", url.Values{"chat_id": {"not a chat"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Must be a number, or @channel.") {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if got := chatOf(t, s, 1); got != "" {
		t.Errorf("saved %q", got)
	}
}

// Telegram's row said only "not configured", with nothing to press, while the
// dashboard's band says "Set up Telegram": an unconfigured row says Set up,
// a configured one doesn't, and Tailnet (nothing to paste) never does.
func TestUnconfiguredIntegrationSaysSetUp(t *testing.T) {
	_, l, _ := telegramSite(t)
	setUp := regexp.MustCompile(`(?s)<a class="row int" href="([^"]+)".*?</a>`)
	rows := func() map[string]bool {
		out := map[string]bool{}
		for _, m := range setUp.FindAllStringSubmatch(l.Get("/settings/integrations").Body.String(), -1) {
			out[m[1]] = strings.Contains(m[0], `<span class="pc-btn pc-sm">Set up</span>`)
		}
		return out
	}
	got := rows()
	if !got["/settings/integrations/telegram"] || got["/settings/integrations/tailnet"] {
		t.Errorf("unconfigured: %v", got)
	}
	saveToken(t, l)
	if rows()["/settings/integrations/telegram"] {
		t.Error("a configured Telegram still says Set up")
	}
}

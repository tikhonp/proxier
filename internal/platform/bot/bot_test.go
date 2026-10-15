package bot_test

import (
	"context"
	"errors"
	"html"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/bot"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

const token = "123456:BOT-token"

// testModule declares /echo (subscriptions: own), a conversation of every
// kind of step, /boom, which fails, and /servers (servers: manage).
type testModule struct{}

func (testModule) Name() string      { return "bottest" }
func (testModule) Migrations() fs.FS { return nil }

func (testModule) Messages() i18n.Messages {
	return i18n.Messages{
		"bottest.echo":    {EN: "Echo something", RU: "Повторить"},
		"bottest.boom":    {EN: "Fail", RU: "Сломаться"},
		"bottest.servers": {EN: "Servers", RU: "Серверы"},
	}
}

func (testModule) BotCommands() []bot.Command {
	pick := func(said string) bot.Handler {
		return func(_ context.Context, r bot.Request) (bot.Reply, error) {
			var choices []bot.Choice
			for _, c := range []string{"A", "B"} {
				choices = append(choices, bot.Choice{Label: c, Then: func(_ context.Context, r bot.Request) (bot.Reply, error) {
					return bot.Reply{Text: "member " + r.Actor + " said " + said + " and picked " + c, Code: "<secret>",
						Link: &bot.Link{Text: "Open", URL: "https://proxier.test/x"}}, nil
				}})
			}
			return bot.Reply{Text: "pick one", Choices: choices}, nil
		}
	}
	return []bot.Command{
		{Name: "echo", Area: access.Subscriptions, Level: access.LevelOwn, Help: "bottest.echo",
			Run: func(ctx context.Context, r bot.Request) (bot.Reply, error) {
				if r.Text != "" {
					return pick(r.Text)(ctx, r)
				}
				return bot.Reply{Text: "say something", Ask: func(ctx context.Context, r bot.Request) (bot.Reply, error) { return pick(r.Text)(ctx, r) }}, nil
			}},
		{Name: "boom", Area: access.Subscriptions, Level: access.LevelOwn, Help: "bottest.boom",
			Run: func(context.Context, bot.Request) (bot.Reply, error) { return bot.Reply{}, errors.New("boom") }},
		{Name: "servers", Area: access.Servers, Level: access.LevelManage, Help: "bottest.servers",
			Run: func(context.Context, bot.Request) (bot.Reply, error) { return bot.Reply{Text: "servers"}, nil }},
	}
}

var _ module.BotCommandDeclarer = testModule{}

type botSite struct {
	*testing.T
	S   *sitetest.Site
	F   *telegram.Fake
	Now time.Time
}

// newBot is a site with the test module whose bot token is set and whose
// member 1 has chat 5.
func newBot(t *testing.T) *botSite {
	t.Helper()
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{testModule{}}})
	f := telegram.NewFake(t, token)
	s.App.Telegram.BaseURL = f.URL()
	b := &botSite{T: t, S: s, F: f, Now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	s.App.Bot.Now = func() time.Time { return b.Now }
	if err := s.App.Settings.Set(t.Context(), "admin", "telegram", map[string]string{"telegram.bot_token": token}); err != nil {
		t.Fatal(err)
	}
	b.chat(1, "5")
	return b
}

func (b *botSite) chat(member int64, chat string) {
	b.Helper()
	if err := b.S.App.Auth.SetTelegramChat(b.Context(), member, chat, "Chat"); err != nil {
		b.Fatal(err)
	}
}

func (b *botSite) poll() {
	b.Helper()
	if err := b.S.App.Bot.Poll(b.Context()); err != nil {
		b.Fatal(err)
	}
}

// say writes text in chat 5 and returns what the bot sent back.
func (b *botSite) say(text string) []telegram.Sent {
	b.Helper()
	return b.in(5, "en", text)
}

func (b *botSite) in(chat int64, lang, text string) []telegram.Sent {
	b.Helper()
	before := len(b.F.Sent())
	b.F.Message(chat, lang, text)
	b.poll()
	return b.F.Sent()[before:]
}

func (b *botSite) tap(messageID int64, data string) []telegram.Sent {
	b.Helper()
	before := len(b.F.Sent())
	b.F.Tap(5, messageID, data)
	b.poll()
	return b.F.Sent()[before:]
}

func one(t *testing.T, sent []telegram.Sent) telegram.Sent {
	t.Helper()
	if len(sent) != 1 {
		t.Fatalf("%d messages, want 1: %+v", len(sent), sent)
	}
	return sent[0]
}

func TestConversationRunsAsTheMember(t *testing.T) {
	b := newBot(t)
	if got := one(t, b.say("/echo")); got.Text != "say something" || got.ChatID != "5" || len(got.Keyboard) != 0 {
		t.Fatalf("%+v", got)
	}
	ask := one(t, b.say("  hello  "))
	if ask.Text != "pick one" || len(ask.Keyboard) != 2 || ask.Keyboard[1][0].Text != "B" || ask.Keyboard[1][0].Data == "" {
		t.Fatalf("%+v", ask)
	}
	done := one(t, b.tap(ask.MessageID, ask.Keyboard[1][0].Data))
	// The tapped message becomes the answer, escaped, its code tappable.
	if !done.Edit || done.MessageID != ask.MessageID || done.ParseMode != "HTML" ||
		done.Text != "member member:1 said hello and picked B\n\n<code>&lt;secret&gt;</code>" ||
		len(done.Keyboard) != 1 || done.ButtonURL != "https://proxier.test/x" {
		t.Fatalf("%+v", done)
	}
	if got := b.F.Answers(); len(got) != 1 || got[0] != "" {
		t.Errorf("callback answers %q", got)
	}
	// It is over: the same button again runs nothing.
	if sent := b.tap(ask.MessageID, ask.Keyboard[0][0].Data); len(sent) != 0 {
		t.Fatalf("%+v", sent)
	}
	if got := b.F.Answers(); got[1] != "This has expired. Start again." {
		t.Errorf("%q", got)
	}
	// Arguments skip the question.
	if got := one(t, b.say("/echo@proxier_test_bot hi there")); got.Text != "pick one" {
		t.Fatalf("%+v", got)
	}
}

func TestPollConfirmsWhatItRead(t *testing.T) {
	b := newBot(t)
	b.say("/help")
	if b.F.Pending() != 0 {
		// The next poll asks with the offset after the last update.
		b.poll()
	}
	if n := b.F.Pending(); n != 0 {
		t.Fatalf("%d updates left", n)
	}
	before := len(b.F.Sent())
	b.poll()
	if len(b.F.Sent()) != before {
		t.Fatal("an update was answered twice")
	}
}

func TestCommandMenuIsSetOncePerBot(t *testing.T) {
	b := newBot(t)
	b.poll()
	b.poll()
	en, ru := b.F.Commands(""), b.F.Commands("ru")
	if len(en) != 4 || en[0] != (telegram.BotCommand{Command: "echo", Description: "Echo something"}) || en[3].Command != "cancel" ||
		len(ru) != 4 || ru[3].Description != "Прервать начатое" {
		t.Fatalf("%+v\n%+v", en, ru)
	}
	n := 0
	for _, c := range b.F.Calls() {
		if c == "setMyCommands" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("setMyCommands %d times, want once per language", n)
	}
}

func TestUnknownChatIsToldHowToBecomeOne(t *testing.T) {
	b := newBot(t)
	got := one(t, b.in(77, "ru", "/start"))
	if got.ChatID != "77" || !strings.Contains(got.Text, "Этот чат не принадлежит участнику Proxier") {
		t.Fatalf("%+v", got)
	}
	if got := one(t, b.in(78, "de", "/echo hi")); !strings.Contains(html.UnescapeString(got.Text), "This chat isn't a Proxier member's.") {
		t.Fatalf("%+v", got)
	}
	// It is heard, for Detect chat.
	chats, err := b.S.App.Bot.Chats(b.Context())
	if err != nil || len(chats) != 2 || chats[0].ID != 77 || chats[1].ID != 78 {
		t.Fatalf("%+v %v", chats, err)
	}
}

func TestGroupChatIsNeverAnswered(t *testing.T) {
	b := newBot(t)
	b.chat(1, "-100") // even the member's own notification chat
	before := len(b.F.Sent())
	b.F.GroupMessage(-100, 5, "/echo hi")
	b.poll()
	if len(b.F.Sent()) != before {
		t.Fatalf("%+v", b.F.Sent()[before:])
	}
	chats, _ := b.S.App.Bot.Chats(b.Context())
	if len(chats) != 1 || chats[0].ID != -100 || chats[0].Type != "group" {
		t.Fatalf("%+v", chats)
	}
}

func TestCommandsFollowTheGrant(t *testing.T) {
	b := newBot(t)
	anna := b.S.Join("anna", access.PresetViewer.Grant().With(access.Subscriptions, access.LevelOwn), "198.51.100.2:1")
	b.chat(anna.Member, "6")
	help := one(t, b.in(6, "en", "/help"))
	if !strings.Contains(help.Text, "/echo — Echo something") || strings.Contains(help.Text, "/servers") {
		t.Fatalf("%s", help.Text)
	}
	if got := one(t, b.in(6, "en", "/servers")); html.UnescapeString(got.Text) != "Your access in Proxier doesn't allow this." {
		t.Fatalf("%+v", got)
	}
	// A member who reaches no command is told their chat is for notifications.
	viewer := b.S.Join("vera", access.PresetViewer.Grant().With(access.Subscriptions, access.LevelNone), "198.51.100.3:1")
	b.chat(viewer.Member, "7")
	if got := one(t, b.in(7, "en", "hello")); got.Text != "Your notifications come to this chat. There are no commands for you here." {
		t.Fatalf("%+v", got)
	}
}

func TestRepliesAreInTheMembersLanguage(t *testing.T) {
	b := newBot(t)
	if err := b.S.App.Auth.SetLanguage(b.Context(), 1, "ru"); err != nil {
		t.Fatal(err)
	}
	if got := one(t, b.in(5, "en", "/cancel")); got.Text != "Прервано." {
		t.Fatalf("%+v", got)
	}
}

func TestRemovedMembersChatIsNoLongerTheirs(t *testing.T) {
	b := newBot(t)
	anna := b.S.Join("anna", access.PresetOwner.Grant(), "198.51.100.2:1")
	b.chat(anna.Member, "6")
	if _, err := b.S.App.Auth.RemoveMember(b.Context(), 1, anna.Member); err != nil {
		t.Fatal(err)
	}
	if got := one(t, b.in(6, "en", "/echo hi")); !strings.Contains(html.UnescapeString(got.Text), "isn't a Proxier member's") {
		t.Fatalf("%+v", got)
	}
}

func TestConversationEndsOnCancelNewCommandOrExpiry(t *testing.T) {
	b := newBot(t)
	ask := one(t, b.say("/echo hi"))
	one(t, b.say("/cancel"))
	if sent := b.tap(ask.MessageID, ask.Keyboard[0][0].Data); len(sent) != 0 {
		t.Fatal("a cancelled conversation's button ran")
	}

	ask = one(t, b.say("/echo hi"))
	one(t, b.say("/echo again"))
	if sent := b.tap(ask.MessageID, ask.Keyboard[0][0].Data); len(sent) != 0 {
		t.Fatal("an earlier conversation's button ran")
	}

	one(t, b.say("/echo"))
	b.Now = b.Now.Add(bot.Expiry)
	// The answer comes too late: it is not taken as the name.
	if got := one(t, b.say("hello")); !strings.HasPrefix(got.Text, "What I can do:") {
		t.Fatalf("%+v", got)
	}
}

func TestFailingCommandApologises(t *testing.T) {
	b := newBot(t)
	if got := one(t, b.say("/boom")); got.Text != "Something went wrong. Try again, or do it in Proxier." {
		t.Fatalf("%+v", got)
	}
	if got := one(t, b.say("/nope")); !strings.HasPrefix(got.Text, "What I can do:") {
		t.Fatalf("%+v", got)
	}
}

func TestNoTokenNoCalls(t *testing.T) {
	b := newBot(t)
	if err := b.S.App.Settings.Set(b.Context(), "admin", "telegram", map[string]string{"telegram.bot_token": ""}); err != nil {
		t.Fatal(err)
	}
	before := len(b.F.Calls())
	if err := b.S.App.Bot.Poll(b.Context()); err == nil {
		t.Fatal("polled without a token")
	}
	if len(b.F.Calls()) != before {
		t.Fatalf("%v", b.F.Calls()[before:])
	}
}

func TestDetectChatKeepsTheNewestChats(t *testing.T) {
	b := newBot(t)
	for i := int64(1); i <= 101; i++ {
		b.Now = b.Now.Add(time.Second)
		b.in(1000+i, "en", "hi")
	}
	chats, err := b.S.App.Bot.Chats(b.Context())
	if err != nil || len(chats) != 100 || chats[0].ID != 1101 || chats[99].ID != 1002 {
		t.Fatalf("%d chats, %v", len(chats), err)
	}
}

package subscriptions_test

import (
	"html"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
)

const botToken = "123456:LINK-token"

// tgChat is a Telegram fake whose bot is the harness's, with member 1's
// private chat 5.
type tgChat struct {
	h *substest.Harness
	f *telegram.Fake
}

func newChat(t *testing.T, h *substest.Harness) *tgChat {
	t.Helper()
	f := telegram.NewFake(t, botToken)
	h.App.Telegram.BaseURL = f.URL()
	if err := h.App.Settings.Set(t.Context(), "admin", "telegram", map[string]string{"telegram.bot_token": botToken}); err != nil {
		t.Fatal(err)
	}
	c := &tgChat{h: h, f: f}
	c.link(1, "5")
	return c
}

func (c *tgChat) link(member int64, chat string) {
	c.h.T.Helper()
	if err := c.h.App.Auth.SetTelegramChat(c.h.T.Context(), member, chat, "Chat"); err != nil {
		c.h.T.Fatal(err)
	}
}

func (c *tgChat) answer() telegram.Sent {
	c.h.T.Helper()
	before := len(c.f.Sent())
	if err := c.h.App.Bot.Poll(c.h.T.Context()); err != nil {
		c.h.T.Fatal(err)
	}
	sent := c.f.Sent()[before:]
	if len(sent) != 1 {
		c.h.T.Fatalf("%d answers, want 1: %+v", len(sent), sent)
	}
	sent[0].Text = html.UnescapeString(sent[0].Text)
	return sent[0]
}

func (c *tgChat) say(chat int64, text string) telegram.Sent {
	c.h.T.Helper()
	c.f.Message(chat, "en", text)
	return c.answer()
}

func (c *tgChat) tap(chat int64, m telegram.Sent, label string) telegram.Sent {
	c.h.T.Helper()
	for _, row := range m.Keyboard {
		if strings.HasPrefix(row[0].Text, label+" ·") {
			c.f.Tap(chat, m.MessageID, row[0].Data)
			return c.answer()
		}
	}
	c.h.T.Fatalf("no button %q in %+v", label, m.Keyboard)
	return telegram.Sent{}
}

func labels(m telegram.Sent) string {
	var out []string
	for _, row := range m.Keyboard {
		if row[0].Data != "" {
			out = append(out, row[0].Text)
		}
	}
	return strings.Join(out, ", ")
}

func liveLinks(t *testing.T, h *substest.Harness) []links.Row {
	t.Helper()
	ls, err := h.Mod.Links.List(t.Context(), access.All, links.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func TestLinkFromTelegram(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Work", 1)
	fam := h.Subscription("Family", 1, 2)
	c := newChat(t, h)

	if got := c.say(5, "/link"); got.Text != "What should the new link be called? For example: Mom — iPhone." || len(got.Keyboard) != 0 {
		t.Fatalf("%+v", got)
	}
	pick := c.say(5, "Mom — iPhone")
	if pick.Text != "Which subscription should “Mom — iPhone” serve?" || labels(pick) != "Family · 2 servers, Work · 1 server" {
		t.Fatalf("%+v", pick)
	}
	done := c.tap(5, pick, "Family")

	ls := liveLinks(t, h)
	if len(ls) != 1 || ls[0].Name != "Mom — iPhone" || ls[0].SubscriptionID != fam || ls[0].OwnerID != 1 || ls[0].State != "active" {
		t.Fatalf("%+v", ls)
	}
	token, err := h.Mod.Links.Token(t.Context(), ls[0].ID, access.All)
	if err != nil {
		t.Fatal(err)
	}
	want := "Link “Mom — iPhone” for Family is ready. Send this URL to the person, or open the link page for its QR code:\n\n" +
		"<code>http://proxier.test/s/" + token + "</code>"
	if !done.Edit || done.MessageID != pick.MessageID || done.Text != want ||
		done.ButtonText != "Open in Proxier" || done.ButtonURL != "http://proxier.test/links/"+strconv.FormatInt(ls[0].ID, 10) {
		t.Fatalf("%+v", done)
	}
	created := h.Events("link.created")
	if len(created) != 1 || created[0].Actor != "member:1" || created[0].Payload["subscription"] != "Family" {
		t.Fatalf("%+v", created)
	}
	if rec := h.Fetch("GET", "/s/"+token, "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "vless://") {
		t.Fatalf("the URL doesn't serve: %d\n%s", rec.Code, rec.Body)
	}
}

func TestLinkNameAfterTheCommandSkipsTheQuestion(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Family", 1)
	c := newChat(t, h)
	if got := c.say(5, "/link Dad — Mac"); got.Text != "Which subscription should “Dad — Mac” serve?" {
		t.Fatalf("%+v", got)
	}
}

func TestTakenLinkNameAsksForAnotherAndKeepsTheSubscription(t *testing.T) {
	h := substest.New(t)
	fam := h.Subscription("Family", 1)
	h.Subscription("Work", 1)
	h.Link(fam, "Mom — iPhone")
	c := newChat(t, h)

	again := c.tap(5, c.say(5, "/link Mom — iPhone"), "Work")
	if again.Text != "Another link has this name. Send another name." || len(again.Keyboard) != 0 {
		t.Fatalf("%+v", again)
	}
	if got := c.say(5, strings.Repeat("x", 81)); got.Text != "At most 80 characters. Send another name." {
		t.Fatalf("%+v", got)
	}
	if got := c.say(5, "Mom — iPad"); !strings.HasPrefix(got.Text, "Link “Mom — iPad” for Work is ready.") || got.Edit {
		t.Fatalf("%+v", got)
	}
	if n := len(h.Events("link.created")); n != 2 {
		t.Fatalf("%d links created, want the harness's and one", n)
	}
}

func TestLinkOffersOnlySubscriptionsTheMemberMayChange(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Family", 1)
	c := newChat(t, h)
	anna := h.Site.Join("anna", access.PresetViewer.Grant().With(access.Subscriptions, access.LevelViewAll), "198.51.100.2:1")
	c.link(anna.Member, "6")

	// Anna reads Family but may not add to it.
	if got := c.say(6, "/link"); got.Text != "There is no subscription you can add a link to." {
		t.Fatalf("%+v", got)
	}
	annas := access.Who{Member: anna.Member, Grant: access.PresetViewer.Grant().With(access.Subscriptions, access.LevelViewAll)}
	h.SubscriptionOf(annas, "Anna's")
	pick := c.say(6, "/link Phone")
	if labels(pick) != "Anna's · 0 servers" {
		t.Fatalf("%+v", pick)
	}
	c.tap(6, pick, "Anna's")
	if ls := liveLinks(t, h); len(ls) != 1 || ls[0].OwnerID != anna.Member {
		t.Fatalf("%+v", ls)
	}
}

func TestLinkToASubscriptionGoneMeanwhileOffersTheRest(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Family", 1)
	work := h.Subscription("Work", 1)
	c := newChat(t, h)
	pick := c.say(5, "/link Phone")
	if err := h.Mod.Subs.Delete(t.Context(), work, access.All, "member:1"); err != nil {
		t.Fatal(err)
	}
	got := c.tap(5, pick, "Work")
	if got.Text != "That subscription is no longer yours to use. Which subscription should “Phone” serve?" || labels(got) != "Family · 1 server" {
		t.Fatalf("%+v", got)
	}
	c.tap(5, got, "Family")
	if ls := liveLinks(t, h); len(ls) != 1 || ls[0].Name != "Phone" {
		t.Fatalf("%+v", ls)
	}
}

func TestLinkCommandNeedsSubscriptionsAccess(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Family", 1)
	c := newChat(t, h)
	vera := h.Site.Join("vera", access.PresetViewer.Grant().With(access.Subscriptions, access.LevelNone), "198.51.100.3:1")
	c.link(vera.Member, "7")
	if got := c.say(7, "/link Phone"); got.Text != "Your access in Proxier doesn't allow this." {
		t.Fatalf("%+v", got)
	}
	if ls := liveLinks(t, h); len(ls) != 0 {
		t.Fatalf("%+v", ls)
	}
}

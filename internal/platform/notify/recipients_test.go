package notify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
)

// Chats of the members the recipient tests add; member 1 (Owner) has chat.
const (
	annaChat  = "201"
	borisChat = "202"
	carolChat = "203"
	daveChat  = "204"
)

var (
	subsOwn       = access.Grant{}.With(access.Subscriptions, access.LevelOwn)
	subsManageAll = access.Grant{}.With(access.Subscriptions, access.LevelManageAll)
	subsViewAll   = access.Grant{}.With(access.Subscriptions, access.LevelViewAll)
	serversManage = access.Grant{}.With(access.Servers, access.LevelManage)
)

func link(id string) events.Subject { return events.Subject{Type: "link", ID: id} }

// A notification about a member's link goes to that member and to the
// members with subscriptions: manage_all who have the type on, and to
// nobody else.
func TestLinkNotificationGoesToItsOwnerAndManageAll(t *testing.T) {
	e := newEnv(t, Options{})
	anna := e.join("anna", subsOwn, i18n.EN, annaChat)
	e.join("boris", subsManageAll, i18n.EN, borisChat)
	e.join("carol", subsViewAll, i18n.EN, carolChat)
	e.join("dave", serversManage, i18n.EN, daveChat)
	other := e.join("erin", subsManageAll, i18n.EN, "205")
	must(t, e.svc.SetRule(bg, other, subsManageAll, "test.link_shared", false))
	e.owners.give(link("5"), anna)
	e.fresh()

	e.record("test.link_shared", link("5"), nil)
	e.settle()
	e.h.Drain()
	got := map[string]int{}
	for _, m := range e.sent(0) {
		got[m.ChatID]++
	}
	want := map[string]int{chat: 1, annaChat: 1, borisChat: 1}
	if len(got) != len(want) {
		t.Fatalf("sent to %v, want %v", got, want)
	}
	for c, n := range want {
		if got[c] != n {
			t.Fatalf("sent to %v, want %v", got, want)
		}
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE event_id = (SELECT max(id) FROM events WHERE type = 'test.link_shared')`); n != 3 {
		t.Fatalf("%d notifications, want one per recipient", n)
	}
}

// Another member's link reaches anna only if she manages all of them.
func TestOwnLevelHearsNothingOfOthersThings(t *testing.T) {
	e := newEnv(t, Options{})
	e.join("anna", subsOwn, i18n.EN, annaChat)
	e.owners.give(link("6"), 1)
	e.fresh()
	e.record("test.link_shared", link("6"), nil)
	e.settle()
	e.h.Drain()
	if got := e.to(annaChat); len(got) != 0 {
		t.Fatalf("anna got member 1's link: %+v", got)
	}
	if got := e.sent(1); len(got) != 1 || got[0].ChatID != chat {
		t.Fatalf("%+v", got)
	}
}

// A member who has no chat gets nothing, and the others still get theirs.
func TestMemberWithoutChatGetsNothing(t *testing.T) {
	e := newEnv(t, Options{})
	e.join("anna", serversManage, i18n.EN, "")
	e.fresh()
	e.down("12")
	e.settle()
	e.h.Drain()
	if got := e.sent(1); len(got) != 1 || got[0].ChatID != chat {
		t.Fatalf("%+v", got)
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE member_id != 1`); n != 0 {
		t.Fatalf("%d notifications queued for a member with no chat", n)
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE state != 'sent'`); n != 0 {
		t.Fatalf("%d notifications not sent", n)
	}
}

// Each recipient reads the message in their own language.
func TestEachRecipientGetsTheirLanguage(t *testing.T) {
	e := newEnv(t, Options{})
	e.join("anna", serversManage, i18n.RU, annaChat)
	e.fresh()
	e.down("12")
	e.settle()
	e.sent(2)
	en, ru := e.to(chat), e.to(annaChat)
	if len(en) != 1 || en[0].Text != "🔴 <b>nl-2 is down</b>\nUnreachable over SSH (timeout)." || en[0].ButtonText != "Open in Proxier" {
		t.Fatalf("member 1: %+v", en)
	}
	if len(ru) != 1 || ru[0].Text != "🔴 <b>nl-2 недоступен</b>\nНедоступен по SSH (timeout)." || ru[0].ButtonText != "Открыть в Proxier" {
		t.Fatalf("anna: %+v", ru)
	}
}

// A removed member gets nothing: neither new notifications nor those queued
// for them before the removal.
func TestRemovedMemberGetsNothing(t *testing.T) {
	e := newEnv(t, Options{NoJobs: true})
	anna := e.join("anna", serversManage, i18n.EN, annaChat)
	e.down("12") // queued for anna too, not yet sent
	e.settle()
	queued := e.count(`SELECT count(*) FROM notifications WHERE member_id = ?`, anna)
	if queued == 0 {
		t.Fatal("nothing was queued for anna before her removal")
	}
	_, err := e.auth.RemoveMember(bg, 1, anna)
	must(t, err)
	later := e.down("13")
	// What is about her, like the handing over of her things, isn't hers
	// any more either.
	e.record("test.link_shared", auth.MemberSubject(anna), nil)
	e.settle()
	e.h.Start(e.h.Sys)
	e.exhaust()
	e.settle()
	if got := e.to(annaChat); len(got) != 0 {
		t.Fatalf("a removed member got %+v", got)
	}
	if got := e.to(chat); len(got) < 3 {
		t.Fatalf("member 1 got only %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE member_id = ? AND state = 'failed'`, anna); n != queued {
		t.Fatalf("%d of anna's %d queued notifications failed", n, queued)
	}
	if n := e.count(`SELECT count(*) FROM notifications WHERE member_id = ? AND event_id >= ?`, anna, later); n != 0 {
		t.Fatalf("%d notifications queued for anna after her removal", n)
	}
}

// One chat failing neither holds up the others nor sends them twice.
func TestOneRecipientFailingLeavesTheOthers(t *testing.T) {
	e := newEnv(t, Options{})
	anna := e.join("anna", serversManage, i18n.EN, annaChat)
	e.fresh()
	e.tg.Block(annaChat)
	e.down("12")
	e.settle()
	e.exhaust()
	if got := e.to(chat); len(got) != 1 {
		t.Fatalf("member 1 got %d messages", len(got))
	}
	failed, err := e.svc.Failed(bg, anna, e.h.Clock.Now().Add(-time.Hour), 10)
	if err != nil || len(failed) != 1 || failed[0].Attempts != 4 {
		t.Fatalf("anna's failed list: %+v %v", failed, err)
	}
	if mine, _ := e.svc.Failed(bg, 1, e.h.Clock.Now().Add(-time.Hour), 10); len(mine) != 0 {
		t.Fatalf("member 1 sees anna's failure: %+v", mine)
	}
	if bad, _ := e.svc.LatestFailed(bg, 1); bad {
		t.Fatal("member 1 is warned of anna's chat")
	}
	if bad, _ := e.svc.LatestFailed(bg, anna); !bad {
		t.Fatal("anna isn't warned of her own chat")
	}
	// Retrying is the recipient's own: member 1 can't, anna can, and it
	// sends to her alone.
	if err := e.svc.Retry(bg, 1, failed[0].ID); err != notify.ErrNotFailed {
		t.Fatalf("member 1 retried anna's notification: %v", err)
	}
	e.tg.Unblock(annaChat)
	must(t, e.svc.Retry(bg, anna, failed[0].ID))
	e.h.Drain()
	if got := e.to(annaChat); len(got) != 1 {
		t.Fatalf("anna got %d after her retry", len(got))
	}
	if got := e.to(chat); len(got) != 1 {
		t.Fatalf("member 1 got %d after anna's retry", len(got))
	}
}

// The new-IP sign-in notice goes only to the member who signed in, even a
// member who manages Settings hears nothing of it.
func TestNewIPNoticeGoesOnlyToThatMember(t *testing.T) {
	e := newEnv(t, Options{})
	anna := e.join("anna", subsOwn, i18n.EN, annaChat)
	e.fresh()
	e.record(auth.SignedInEvent, auth.MemberSubject(anna), map[string]any{"ip": "203.0.113.7", "user_agent": "x", "new_ip": true})
	e.settle()
	e.h.Drain()
	got := e.sent(1)
	if len(got) != 1 || got[0].ChatID != annaChat || !strings.Contains(got[0].Text, "203.0.113.7") {
		t.Fatalf("%+v", got)
	}
}

// A member's being added goes to who manages Settings, not to who manages
// only servers, whose platform events they otherwise get.
func TestMemberEventsGoToSettingsManagers(t *testing.T) {
	e := newEnv(t, Options{})
	e.join("dave", serversManage, i18n.EN, daveChat)
	e.fresh()
	e.record("member.added", auth.MemberSubject(9), map[string]any{"username": "zoe", "access": "viewer"})
	e.down("12")
	e.settle()
	e.h.Drain()
	e.sent(3)
	if got := e.to(chat); len(got) != 2 || !strings.Contains(got[0].Text, "zoe was added") {
		t.Fatalf("member 1 got %+v", got)
	}
	if got := e.to(daveChat); len(got) != 1 || !strings.Contains(got[0].Text, "nl-2 is down") {
		t.Fatalf("dave got %+v", got)
	}
}

// Each member's burst is their own group: one message each, in their own
// chat.
func TestGroupsArePerRecipient(t *testing.T) {
	e := newEnv(t, Options{})
	e.join("anna", serversManage, i18n.RU, annaChat)
	e.fresh()
	for _, id := range []string{"12", "13"} {
		e.health(id, "blocked")
	}
	e.settle()
	e.h.Drain()
	e.h.Clock.Advance(notify.GroupWindow)
	e.h.Drain()
	en, ru := e.to(chat), e.to(annaChat)
	if len(en) != 1 || !strings.Contains(en[0].Text, "2 servers are blocked") {
		t.Fatalf("member 1: %+v", en)
	}
	if len(ru) != 1 || !strings.Contains(ru[0].Text, "2 сервера заблокированы") {
		t.Fatalf("anna: %+v", ru)
	}
}

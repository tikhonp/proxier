package pages_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var ownSubs = access.Grant{}.With(access.Subscriptions, access.LevelOwn)

func linkPath(id int64) string { return "/links/" + strconv.FormatInt(id, 10) }
func subPath(id int64) string  { return "/subscriptions/" + strconv.FormatInt(id, 10) }

// Two members at own each see only their own subscriptions and links;
// the other's by URL is 404, not 403, so IDs don't tell what exists.
func TestOwnMembersSeeOnlyTheirOwn(t *testing.T) {
	h := substest.New(t)
	anna := h.Site.Join("anna", ownSubs, "") // member 2
	bob := h.Site.Join("bob", ownSubs, "")   // member 3
	annaW, bobW := access.Who{Member: 2, Grant: ownSubs}, access.Who{Member: 3, Grant: ownSubs}
	annaSub := h.SubscriptionOf(annaW, "Anna home", 1)
	annaLink, _ := h.LinkOf(annaW, annaSub, "Anna phone")
	bobSub := h.SubscriptionOf(bobW, "Bob home", 2)
	bobLink, _ := h.LinkOf(bobW, bobSub, "Bob phone")

	subsPage := anna.Get("/subscriptions").Body.String()
	links := anna.Get("/links").Body.String()
	if !strings.Contains(subsPage, ">Anna home<") || !strings.Contains(links, ">Anna phone<") {
		t.Fatal("anna doesn't see her own")
	}
	if strings.Contains(subsPage, "Bob home") || strings.Contains(links, "Bob phone") {
		t.Error("anna sees bob's")
	}
	for _, path := range []string{linkPath(bobLink), linkPath(bobLink) + "/edit", linkPath(bobLink) + "/url", subPath(bobSub), subPath(bobSub) + "/delete"} {
		if rec := anna.Get(path); rec.Code != http.StatusNotFound {
			t.Errorf("anna GET %s: %d, want 404", path, rec.Code)
		}
	}
	if rec := anna.Post(linkPath(bobLink)+"/disable", nil); rec.Code != http.StatusNotFound {
		t.Errorf("anna disabling bob's link: %d, want 404", rec.Code)
	}
	if l, _ := h.Mod.Links.Get(t.Context(), bobLink, access.All); l.State != "active" {
		t.Fatalf("bob's link is %s", l.State)
	}
	if rec := bob.Get(linkPath(annaLink)); rec.Code != http.StatusNotFound {
		t.Errorf("bob opening anna's link: %d", rec.Code)
	}
	hits, err := h.Mod.Search(t.Context(), annaW, "home", 10)
	if err != nil || len(hits) != 1 || hits[0].Label != "Anna home" {
		t.Errorf("anna's search: %+v %v", hits, err)
	}

	// a new link goes only into a subscription of her own
	newPage := anna.Get("/links/new").Body.String()
	if !strings.Contains(newPage, ">Anna home ·") || strings.Contains(newPage, "Bob home") {
		t.Error("New link offers another member's subscription")
	}
	anna.Post("/links", url.Values{"name": {"Sneaky"}, "subscription": {strconv.FormatInt(bobSub, 10)}, "expiry_mode": {"never"}, "language": {"en"}})
	if n := len(h.Events("link.created")); n != 2 {
		t.Errorf("a link in another member's subscription was created (%d link.created)", n)
	}
}

// A Viewer reads everyone's subscriptions and links but changes only their
// own: no controls on another member's, and 403 when forged.
func TestViewAllReadsButDoesNotChange(t *testing.T) {
	h := substest.New(t)
	viewer := h.Site.Join("vera", access.PresetViewer.Grant(), "") // member 2
	sub := h.Subscription("Family", 1)
	link, _ := h.Link(sub, "Mom")

	page := viewer.Get(linkPath(link))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "owner "+sitetest.Username) {
		t.Fatalf("the viewer reading the owner's link: %d", page.Code)
	}
	for _, control := range []string{`data-action="links.disable"`, `data-action="links.delete"`, `data-action="links.edit"`} {
		if strings.Contains(page.Body.String(), control) {
			t.Errorf("the viewer is offered %s", control)
		}
		if !strings.Contains(h.Login.Get(linkPath(link)).Body.String(), control) {
			t.Errorf("the owner isn't offered %s", control)
		}
	}
	if rec := viewer.Get(subPath(sub)); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `action="`+subPath(sub)+`"`) {
		t.Errorf("the viewer reading the subscription: %d, or offered its settings", rec.Code)
	}
	for _, path := range []string{linkPath(link) + "/disable", linkPath(link) + "/delete", subPath(sub) + "/delete"} {
		if rec := viewer.Post(path, nil); rec.Code != http.StatusForbidden {
			t.Errorf("viewer POST %s: %d, want 403", path, rec.Code)
		}
	}
	for _, path := range []string{linkPath(link) + "/edit", linkPath(link) + "/alerts", subPath(sub) + "/delete"} {
		if rec := viewer.Get(path); rec.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s: %d, want 403", path, rec.Code)
		}
	}
	if l, _ := h.Mod.Links.Get(t.Context(), link, access.All); l.State != "active" {
		t.Fatalf("the owner's link is %s", l.State)
	}
	if n := len(h.Events("link.disabled")) + len(h.Events("link.deleted")) + len(h.Events("subscription.deleted")); n != 0 {
		t.Errorf("%d changes recorded", n)
	}
	// their own they change
	w := access.Who{Member: 2, Grant: access.PresetViewer.Grant()}
	own, _ := h.LinkOf(w, h.SubscriptionOf(w, "Vera", 1), "Vera phone")
	if rec := viewer.Post(linkPath(own)+"/disable", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("the viewer disabling their own link: %d", rec.Code)
	}
}

// A cut-off rotates shared servers: without servers: manage it isn't
// offered and is refused, even on the member's own link.
func TestCutOffNeedsServersManage(t *testing.T) {
	h := substest.New(t)
	g := access.Grant{}.With(access.Subscriptions, access.LevelManageAll).With(access.Servers, access.LevelView)
	m := h.Site.Join("max", g, "")
	w := access.Who{Member: 2, Grant: g}
	link, _ := h.LinkOf(w, h.SubscriptionOf(w, "Max", 1), "Max phone")

	if strings.Contains(m.Get(linkPath(link)).Body.String(), `data-action="cutoff.action"`) {
		t.Error("Cut off is offered without servers: manage")
	}
	if !strings.Contains(h.Login.Get(linkPath(link)).Body.String(), `data-action="cutoff.action"`) {
		t.Error("Cut off isn't offered to the owner")
	}
	if rec := m.Get(linkPath(link) + "/cutoff"); rec.Code != http.StatusForbidden {
		t.Errorf("the cut-off page: %d, want 403", rec.Code)
	}
	if rec := m.Post(linkPath(link)+"/cutoff", nil); rec.Code != http.StatusForbidden {
		t.Errorf("cut-off POST: %d, want 403", rec.Code)
	}
	if n := len(h.Events("link.cut_off")) + len(h.Events("link.disabled")); n != 0 {
		t.Errorf("the link was cut off anyway (%d events)", n)
	}
	// disabling stays theirs
	if rec := m.Post(linkPath(link)+"/disable", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("disabling their own link: %d", rec.Code)
	}
}

// A removed member's subscriptions and links go to the heir; the links keep
// serving, and Activity still names the removed member.
func TestRemovedMembersThingsGoToTheHeir(t *testing.T) {
	h := substest.New(t)
	h.Site.Join("anna", ownSubs, "")
	annaW := access.Who{Member: 2, Grant: ownSubs}
	sub := h.SubscriptionOf(annaW, "Anna home", 1)
	link, token := h.LinkOf(annaW, sub, "Anna phone")
	if _, err := h.App.Auth.RemoveMember(t.Context(), 1, 2); err != nil {
		t.Fatal(err)
	}
	removed, err := events.List(t.Context(), h.App.DB.R, events.Filter{Type: "member.removed"})
	if err != nil || len(removed) != 1 {
		t.Fatalf("member.removed: %v %v", removed, err)
	}
	handover := h.Mod.Subs.HandOverSubscriber()
	for range 2 { // a second delivery moves nothing and records nothing
		if err := h.App.DB.Write(t.Context(), func(tx *sqlx.Tx) error { return handover.Handle(t.Context(), tx, removed[0]) }); err != nil {
			t.Fatal(err)
		}
	}
	ev := h.Events("subscription.handed_over")
	if len(ev) != 1 || ev[0].Payload["from"] != "anna" || ev[0].Payload["to"] != sitetest.Username ||
		ev[0].Payload["subscriptions"] != float64(1) || ev[0].Payload["links"] != float64(1) || ev[0].Subject.String() != "member:2" {
		t.Fatalf("handed_over: %+v", ev)
	}
	if s, _ := h.Mod.Subs.Get(t.Context(), sub, access.Owner(1)); s.OwnerID != 1 {
		t.Error("the subscription isn't the heir's")
	}
	if l, err := h.Mod.Links.Get(t.Context(), link, access.Owner(1)); err != nil || l.OwnerID != 1 {
		t.Errorf("the link isn't the heir's: %v", err)
	}
	if rec := h.Fetch("GET", "/s/"+token, "", "Happ/1.0"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "vless://") {
		t.Errorf("the removed member's link stopped serving: %d", rec.Code)
	}
	activity := h.Login.Get("/activity").Body.String()
	if !strings.Contains(activity, `class="text">anna</a>  to `+sitetest.Username) {
		t.Error("Activity doesn't name the removed member")
	}
}

package links_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

var bg = context.Background()

// noLeak fails when the token is in an event, a job or a notification.
func noLeak(t *testing.T, h *substest.Harness, token string) {
	t.Helper()
	for _, q := range []string{
		`SELECT count(*) FROM events WHERE instr(payload, ?) > 0`,
		`SELECT count(*) FROM jobs WHERE instr(payload, ?) > 0`,
		`SELECT count(*) FROM notifications WHERE instr(message, ?) > 0`,
	} {
		var n int
		if err := h.App.DB.R.GetContext(bg, &n, q, token); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Errorf("the token is in: %s", q)
		}
	}
}

func fieldErr(t *testing.T, err error, field, key string) {
	t.Helper()
	var fe store.FieldErrors
	if !errors.As(err, &fe) || fe[field] != key {
		t.Errorf("want %s: %s, got %v", field, key, err)
	}
}

func TestCreateLink(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1, 2)
	exp := time.Date(2026, 12, 1, 21, 0, 0, 0, time.UTC)
	id, err := h.Mod.Links.Create(bg, links.New{Name: "  Mom — iPhone ", SubscriptionID: sub, Expires: exp, Lang: "ru", Note: "her phone"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	l, err := h.Mod.Links.Get(bg, id)
	if err != nil || l.State != "active" || l.Name != "Mom — iPhone" || l.Lang != "ru" || !l.Expires.Equal(exp) || l.SubscriptionID != sub {
		t.Fatalf("link %+v %v", l, err)
	}
	token, err := h.Mod.Links.Token(bg, id)
	if err != nil || len(token) != 43 {
		t.Fatalf("token %q %v", token, err)
	}
	if u := h.Mod.Links.URL(token); u != "http://proxier.test/s/"+token {
		t.Errorf("URL %s", u)
	}
	if rec := h.Fetch("GET", "/s/"+token, "", ""); rec.Code != 200 || len(strings.Split(strings.TrimSpace(rec.Body.String()), "\n")) != 2 {
		t.Errorf("fetch %d %q", rec.Code, rec.Body.String())
	}
	ev := h.Events("link.created")
	if len(ev) != 1 || ev[0].Payload["subscription"] != "Family" || ev[0].Payload["expiry"] != "2026-12-01T21:00:00.000Z" || ev[0].Subject.String() != "link:1" {
		t.Errorf("events %+v", ev)
	}
	// a second link without expiry: "" in the payload, another token
	id2, token2 := h.Link(sub, "Dad")
	if ev := h.Events("link.created"); len(ev) != 2 || ev[1].Payload["expiry"] != "" {
		t.Errorf("events %+v", ev)
	}
	if token2 == token || id2 == id {
		t.Error("the same token twice")
	}
	noLeak(t, h, token)
	noLeak(t, h, token2)
	// the stored blob is sealed, not the token
	var blob []byte
	_ = h.App.DB.R.GetContext(bg, &blob, `SELECT token FROM subs_links WHERE id = ?`, id)
	if strings.Contains(string(blob), token) || len(blob) == 0 {
		t.Error("the token is stored in clear")
	}
	// bad input writes nothing
	_, err = h.Mod.Links.Create(bg, links.New{Name: "", SubscriptionID: 99, Lang: "de", Expires: h.Now.Add(-time.Hour)}, "admin")
	var fe store.FieldErrors
	if !errors.As(err, &fe) || fe["name"] == "" || fe["subscription"] == "" || fe["language"] == "" || fe["expiry"] == "" {
		t.Errorf("errors %v", err)
	}
}

func TestLinkNamesAreUnique(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, _ := h.Link(sub, "Mom")
	_, err := h.Mod.Links.Create(bg, links.New{Name: "Mom", SubscriptionID: sub, Lang: "en"}, "admin")
	fieldErr(t, err, "name", "links.err.name_taken")
	if _, err := h.Mod.Links.Create(bg, links.New{Name: "mom", SubscriptionID: sub, Lang: "en"}, "admin"); err != nil {
		t.Errorf("letter case counts: %v", err)
	}
	if err := h.Mod.Links.Delete(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mod.Links.Create(bg, links.New{Name: "Mom", SubscriptionID: sub, Lang: "en"}, "admin"); err != nil {
		t.Errorf("a deleted link's name: %v", err)
	}
}

func TestRegenerateToken(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, old := h.Link(sub, "Mom")
	if err := h.Mod.Links.RegenerateToken(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.Mod.Links.Token(bg, id)
	if err != nil || fresh == old {
		t.Fatalf("token %v", err)
	}
	if rec := h.Fetch("GET", "/s/"+old, "", ""); rec.Code != 404 {
		t.Errorf("old URL: %d", rec.Code)
	}
	if rec := h.Fetch("GET", "/s/"+fresh, "", ""); rec.Code != 200 {
		t.Errorf("new URL: %d", rec.Code)
	}
	if ev := h.Events("link.token_regenerated"); len(ev) != 1 || len(ev[0].Payload) != 0 {
		t.Errorf("events %+v", ev)
	}
	noLeak(t, h, old)
	noLeak(t, h, fresh)
}

func TestChangeSubscription(t *testing.T) {
	h := substest.New(t)
	family := h.Subscription("Family", 1)
	friends := h.Subscription("Friends", 2)
	id, token := h.Link(family, "Alex")
	if err := h.Mod.Links.ChangeSubscription(bg, id, friends, "admin"); err != nil {
		t.Fatal(err)
	}
	body := h.Fetch("GET", "/s/"+token, "", "").Body.String()
	if !strings.Contains(body, "de-1.hosts") || strings.Contains(body, "nl-1.hosts") {
		t.Errorf("serves %q", body)
	}
	ev := h.Events("link.subscription_changed")
	if len(ev) != 1 || ev[0].Payload["from"] != "Family" || ev[0].Payload["to"] != "Friends" {
		t.Errorf("events %+v", ev)
	}
	// the same subscription again changes nothing
	if err := h.Mod.Links.ChangeSubscription(bg, id, friends, "admin"); err != nil || len(h.Events("link.subscription_changed")) != 1 {
		t.Errorf("no-op: %v", err)
	}
}

func TestExpiryRules(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, _ := h.Link(sub, "Mom")
	zone := h.Mod.Links.Zone(bg)
	if zone.String() != "Europe/Moscow" {
		t.Fatalf("zone %s", zone)
	}
	// in the past: refused by the form and by the service
	_, err := links.ParseExpiry("on", "2026-10-01", "", zone, h.Now)
	fieldErr(t, err, "expiry", "links.err.expiry_past")
	fieldErr(t, h.Mod.Links.SetExpiry(bg, id, h.Now.Add(-time.Minute), "admin"), "expiry", "links.err.expiry_past")
	_, err = links.ParseExpiry("on", "1 Dec", "", zone, h.Now)
	fieldErr(t, err, "expiry", "links.err.expiry_date")
	_, err = links.ParseExpiry("on", "2026-12-01", "25:00", zone, h.Now)
	fieldErr(t, err, "expiry", "links.err.expiry_time")
	if at, err := links.ParseExpiry("never", "2026-12-01", "", zone, h.Now); err != nil || !at.IsZero() {
		t.Errorf("never: %v %v", at, err)
	}
	// a date and a time are read in Europe/Moscow
	at, err := links.ParseExpiry("on", "2026-12-01", "18:00", zone, h.Now)
	if err != nil || !at.Equal(time.Date(2026, 12, 1, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("date and time: %v %v", at, err)
	}
	day, _ := links.ParseExpiry("on", "2026-12-01", "", zone, h.Now)
	if !day.Equal(time.Date(2026, 12, 1, 21, 0, 0, 0, time.UTC)) || !links.DateOnly(day, zone) || links.DateOnly(at, zone) {
		t.Errorf("date only: %v", day)
	}
	if err := h.Mod.Links.SetExpiry(bg, id, at, "admin"); err != nil {
		t.Fatal(err)
	}
	// any change clears the warning marks
	h.Exec(`UPDATE subs_links SET expiry_warned_at = '2026-11-28T15:00:00.000Z', expired_at = '2026-12-01T15:00:00.000Z' WHERE id = ?`, id)
	if err := h.Mod.Links.SetExpiry(bg, id, day, "admin"); err != nil {
		t.Fatal(err)
	}
	var marks int
	_ = h.App.DB.R.GetContext(bg, &marks, `SELECT count(*) FROM subs_links WHERE expiry_warned_at IS NULL AND expired_at IS NULL AND id = ?`, id)
	if marks != 1 {
		t.Error("the warning marks were kept")
	}
	if err := h.Mod.Links.SetExpiry(bg, id, time.Time{}, "admin"); err != nil {
		t.Fatal(err)
	}
	ev := h.Events("link.expiry_changed")
	if len(ev) != 3 || ev[0].Payload["from"] != "" || ev[0].Payload["to"] != "2026-12-01T15:00:00.000Z" ||
		ev[2].Payload["from"] != "2026-12-01T21:00:00.000Z" || ev[2].Payload["to"] != "" {
		t.Errorf("events %+v", ev)
	}
	// clearing again changes nothing
	if err := h.Mod.Links.SetExpiry(bg, id, time.Time{}, "admin"); err != nil || len(h.Events("link.expiry_changed")) != 3 {
		t.Errorf("no-op: %v", err)
	}
}

func TestEditLink(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, _ := h.Link(sub, "Mom")
	h.Link(sub, "Dad")
	if err := h.Mod.Links.Edit(bg, id, links.Edit{Name: "Mom — iPhone", Lang: "en", Format: "uri-base64"}, "admin"); err != nil {
		t.Fatal(err)
	}
	ev := h.Events("link.changed")
	if len(ev) != 1 || ev[0].Payload["fields"] != "name, format" {
		t.Fatalf("events %+v", ev)
	}
	if err := h.Mod.Links.Edit(bg, id, links.Edit{Name: "Mom — iPhone", Lang: "en", Format: "uri-base64"}, "admin"); err != nil || len(h.Events("link.changed")) != 1 {
		t.Errorf("no change recorded something: %v", err)
	}
	if err := h.Mod.Links.Edit(bg, id, links.Edit{Name: "Mom — iPhone", Lang: "ru", Note: "new phone", Format: "uri-base64"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if ev := h.Events("link.changed"); len(ev) != 2 || ev[1].Payload["fields"] != "note, language" {
		t.Errorf("events %+v", ev)
	}
	fieldErr(t, h.Mod.Links.Edit(bg, id, links.Edit{Name: "Dad", Lang: "en"}, "admin"), "name", "links.err.name_taken")
	fieldErr(t, h.Mod.Links.Edit(bg, id, links.Edit{Name: "Mom", Lang: "en", Format: "mihomo"}, "admin"), "format", "links.err.format")
	fieldErr(t, h.Mod.Links.Edit(bg, id, links.Edit{Name: strings.Repeat("x", 81), Lang: "en"}, "admin"), "name", "links.err.name_long")
	l, _ := h.Mod.Links.Get(bg, id)
	if l.Name != "Mom — iPhone" || l.Lang != "ru" || l.Format != "uri-base64" || l.Note != "new phone" {
		t.Errorf("link %+v", l)
	}
}

func TestDeletedLinkIsReadOnly(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	other := h.Subscription("Friends", 2)
	id, _ := h.Link(sub, "Mom")
	if err := h.Mod.Links.Disable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.Disable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.Events("link.disabled")); n != 1 {
		t.Errorf("disabling a disabled link recorded %d events", n)
	}
	if err := h.Mod.Links.Delete(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"disable":      h.Mod.Links.Disable(bg, id, "admin"),
		"enable":       h.Mod.Links.Enable(bg, id, "admin"),
		"regenerate":   h.Mod.Links.RegenerateToken(bg, id, "admin"),
		"subscription": h.Mod.Links.ChangeSubscription(bg, id, other, "admin"),
		"expiry":       h.Mod.Links.SetExpiry(bg, id, h.Now.Add(time.Hour), "admin"),
		"edit":         h.Mod.Links.Edit(bg, id, links.Edit{Name: "Mom", Lang: "en"}, "admin"),
		"delete":       h.Mod.Links.Delete(bg, id, "admin"),
	} {
		if !errors.Is(err, links.ErrDeleted) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.Mod.Links.Token(bg, id); !errors.Is(err, links.ErrDeleted) {
		t.Errorf("token: %v", err)
	}
	if n := len(h.Events("link.deleted")); n != 1 {
		t.Errorf("%d link.deleted", n)
	}
}

func TestDeleteWithLinksNeedsMove(t *testing.T) {
	h := substest.New(t)
	friends := h.Subscription("Friends", 1)
	family := h.Subscription("Family", 2)
	for _, n := range []string{"Alex", "Sam", "Kim"} {
		h.Link(friends, n)
	}
	body := h.Login.Get("/subscriptions/1/delete").Body.String()
	if !strings.Contains(body, "Move links to…") || !strings.Contains(body, `action="/subscriptions/1/move-links"`) || !strings.Contains(body, ">Family</option>") {
		t.Fatal("the delete page offers no Move links to…")
	}
	if rec := h.Login.Post("/subscriptions/1/delete", nil); rec.Code != 409 {
		t.Errorf("delete with links: %d", rec.Code)
	}
	rec := h.Login.Post("/subscriptions/1/move-links", url.Values{"to": {"2"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/subscriptions/1/delete" {
		t.Fatalf("move: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if ev := h.Events("link.subscription_changed"); len(ev) != 3 || ev[0].Payload["to"] != "Family" {
		t.Errorf("events %+v", ev)
	}
	if list, _ := h.Mod.Links.OfSubscription(bg, family); len(list) != 3 {
		t.Errorf("Family holds %d links", len(list))
	}
	if rec := h.Login.Post("/subscriptions/1/delete", nil); rec.Code != 303 {
		t.Errorf("delete after the move: %d", rec.Code)
	}
	if _, err := h.Mod.Subs.Get(bg, friends); err == nil {
		t.Error("Friends is still there")
	}
}

func TestTombstoneSettingApplies(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, token := h.Link(sub, "Mom")
	if err := h.Mod.Links.Delete(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Advance(8 * 24 * time.Hour)
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.tombstone": "168h0m0s"}); err != nil {
		t.Fatal(err)
	}
	if rec := h.Fetch("GET", "/s/"+token, "", ""); rec.Code != 404 {
		t.Errorf("past a 7-day tombstone: %d", rec.Code)
	}
	if body := h.Login.Get("/links/1").Body.String(); !strings.Contains(body, "Its tombstone ended on 14 Oct 2026") {
		t.Error("the page does not name the end it passed")
	}
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.tombstone": "720h0m0s"}); err != nil {
		t.Fatal(err)
	}
	rec := h.Fetch("GET", "/s/"+token, "", "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != output.StubURI("⛔ Link removed") {
		t.Errorf("inside the default 30 days: %d %q", rec.Code, rec.Body.String())
	}
	// past the tombstone the subscription can go; the link has none left
	h.Advance(23 * 24 * time.Hour)
	if err := h.Mod.Subs.Delete(bg, sub, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.tombstone": "8760h0m0s"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := h.Mod.Links.Get(bg, id); l.SubscriptionID != 0 {
		t.Fatalf("subscription %d", l.SubscriptionID)
	}
	if rec := h.Fetch("GET", "/s/"+token, "", ""); rec.Code != 404 {
		t.Errorf("without a subscription: %d", rec.Code)
	}
	if rec := h.Login.Get("/links/1"); rec.Code != 200 {
		t.Errorf("its page: %d", rec.Code)
	}
}

package subs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

var bg = context.Background()

// en is a context whose localizer is English in Moscow time, as the admin's page is.
func en(h *substest.Harness) context.Context {
	return i18n.WithLocalizer(bg, h.App.I18n.Localizer(i18n.EN, h.App.Cfg.TZ))
}

func settingsOf(s subs.Subscription) subs.Settings {
	return subs.Settings{
		Name: s.Name, Title: s.Title, Description: s.Description, Formats: s.Formats, DefaultFormat: s.DefaultFormat,
		UpdateHours: s.UpdateHours, HideOn: s.Hide.On, HideStates: s.Hide.States, GraceMinutes: int(s.Hide.Grace / time.Minute), AutoAdd: s.AutoAdd,
	}
}

func get(t *testing.T, h *substest.Harness, id int64) subs.Subscription {
	t.Helper()
	s, err := h.Mod.Subs.Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateAndPreview(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1, 2)
	s := get(t, h, id)
	if s.Title != "Family" || s.DefaultFormat != "uri-plain" || len(s.Formats) != 2 || s.UpdateHours != 12 || s.Hide.On ||
		strings.Join(s.Hide.States, ",") != "blocked,down" || s.Hide.Grace != 30*time.Minute || s.AutoAdd {
		t.Fatalf("defaults: %+v", s)
	}
	resp, members, err := h.Mod.Subs.Preview(en(h), id, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || len(resp.Lines) != 2 || !strings.HasSuffix(resp.Lines[0], "Netherlands%201") || !strings.HasSuffix(resp.Lines[1], "Germany%201") {
		t.Fatalf("lines: %v", resp.Lines)
	}
	if !strings.Contains(resp.Lines[0], substest.Credential(1)) {
		t.Error("an unmasked preview hides the credential")
	}
	if resp.Headers[0].Value != "base64:RmFtaWx5" { // "Family"
		t.Errorf("title header: %+v", resp.Headers[0])
	}
	if ev := h.Events("subscription.created"); len(ev) != 1 || ev[0].Payload["name"] != "Family" || ev[0].Actor != "admin" || ev[0].Subject.String() != "subscription:1" {
		t.Errorf("created: %+v", ev)
	}
}

func TestSubscriptionNameRules(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Family")
	for name, want := range map[string]string{
		"   ":                   "subs.err.name_required",
		strings.Repeat("я", 61): "subs.err.name_long",
		"Family":                "subs.err.name_taken",
		" Family ":              "subs.err.name_taken",
	} {
		_, err := h.Mod.Subs.Create(bg, name, "", "", "admin")
		var fe store.FieldErrors
		if !errors.As(err, &fe) || fe["name"] != want {
			t.Errorf("%q: %v, want %s", name, err, want)
		}
	}
	// 60 characters and another letter case are fine
	if _, err := h.Mod.Subs.Create(bg, strings.Repeat("я", 60), "", "", "admin"); err != nil {
		t.Error(err)
	}
	if _, err := h.Mod.Subs.Create(bg, "family", strings.Repeat("t", 61), "", "admin"); err == nil {
		t.Error("a 61-character title was accepted")
	}
	if _, err := h.Mod.Subs.Create(bg, "family", "", "", "admin"); err != nil {
		t.Errorf("another letter case: %v", err)
	}
	if n := len(h.Events("subscription.created")); n != 3 {
		t.Errorf("%d created", n)
	}
	// a rename onto a taken name is refused too, and writes nothing
	friends := h.Subscription("Friends")
	st := settingsOf(get(t, h, friends))
	st.Name = "Family"
	var fe store.FieldErrors
	if _, err := h.Mod.Subs.Update(bg, friends, st, "admin"); !errors.As(err, &fe) || fe["name"] != "subs.err.name_taken" {
		t.Errorf("rename onto a taken name: %v", err)
	}
	if get(t, h, friends).Name != "Friends" {
		t.Error("the refused rename was written")
	}
}

func TestDefaultFormatMustBeAllowed(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family")
	st := settingsOf(get(t, h, id))
	st.Formats = []string{"uri-base64"}
	_, err := h.Mod.Subs.Update(bg, id, st, "admin")
	var fe store.FieldErrors
	if !errors.As(err, &fe) || fe["default_format"] != "subs.err.default_not_allowed" {
		t.Fatalf("turning off the default: %v", err)
	}
	st.Formats = nil
	if _, err := h.Mod.Subs.Update(bg, id, st, "admin"); !errors.As(err, &fe) || fe["formats"] != "subs.err.no_format" {
		t.Fatalf("no format: %v", err)
	}
	st.Formats, st.DefaultFormat = []string{"uri-base64"}, "uri-base64"
	if changed, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil || !changed {
		t.Fatalf("with base64 as the default: %v %v", changed, err)
	}
	if s := get(t, h, id); strings.Join(s.Formats, ",") != "uri-base64" || s.DefaultFormat != "uri-base64" {
		t.Errorf("%+v", s)
	}
}

func TestUpdateRecordsChanges(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family")
	st := settingsOf(get(t, h, id))
	st.Title, st.UpdateHours, st.HideOn, st.GraceMinutes = "Семья", 24, true, 45
	st.HideStates = []string{"down", "blocked", "unknown"} // stored in their own order
	changed, err := h.Mod.Subs.Update(bg, id, st, "admin")
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	ev := h.Events("subscription.updated")
	if len(ev) != 1 || ev[0].Payload["changes"] != "title, update interval, hide unhealthy, hidden states, grace" {
		t.Fatalf("updated: %+v", ev)
	}
	if s := get(t, h, id); strings.Join(s.Hide.States, ",") != "blocked,down,unknown" {
		t.Errorf("states %v", s.Hide.States)
	}
	if changed, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil || changed {
		t.Fatalf("again: %v %v", changed, err)
	}
	if n := len(h.Events("subscription.updated")); n != 1 {
		t.Errorf("an unchanged save recorded: %d", n)
	}
	// bounds
	for _, c := range []struct {
		mut   func(*subs.Settings)
		field string
	}{
		{func(s *subs.Settings) { s.UpdateHours = 0 }, "update_hours"},
		{func(s *subs.Settings) { s.UpdateHours = 169 }, "update_hours"},
		{func(s *subs.Settings) { s.GraceMinutes = 10081 }, "grace"},
		{func(s *subs.Settings) { s.GraceMinutes = -1 }, "grace"},
		{func(s *subs.Settings) { s.HideStates = []string{"paused"} }, "hide_states"},
		{func(s *subs.Settings) { s.Description = strings.Repeat("d", 1001) }, "description"},
	} {
		x := st
		c.mut(&x)
		_, err := h.Mod.Subs.Update(bg, id, x, "admin")
		var fe store.FieldErrors
		if !errors.As(err, &fe) || fe[c.field] == "" {
			t.Errorf("%s: %v", c.field, err)
		}
	}
	// auto add records when it was turned on, and forgets it when off
	st.AutoAdd = true
	h.Advance(time.Hour)
	if _, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil {
		t.Fatal(err)
	}
	if s := get(t, h, id); !s.AutoAdd || !s.AutoAddSince.Equal(h.Now) {
		t.Errorf("auto add since %v, want %v", s.AutoAddSince, h.Now)
	}
	h.Advance(time.Hour)
	if _, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil {
		t.Fatal(err)
	}
	if s := get(t, h, id); !s.AutoAddSince.Equal(h.Now.Add(-time.Hour)) {
		t.Error("saving again moved auto_add_since")
	}
	st.AutoAdd = false
	if _, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil {
		t.Fatal(err)
	}
	if s := get(t, h, id); s.AutoAdd || !s.AutoAddSince.IsZero() {
		t.Errorf("auto add off: %+v", s)
	}
}

func TestDeleteWaitsForLinksAndTombstones(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1, 2)
	day := 24 * time.Hour
	at := func(d time.Duration) string { return h.Now.Add(-d).UTC().Format("2006-01-02T15:04:05.000Z") }
	ins := `INSERT INTO subs_links (subscription_id, name, state, language, created_at, deleted_at) VALUES (?, ?, ?, 'ru', ?, ?)`

	h.Exec(ins, id, "Mom", "active", at(40*day), nil)
	if err := h.Mod.Subs.Delete(bg, id, "admin"); !errors.Is(err, subs.ErrHasLinks) {
		t.Fatalf("with a live link: %v", err)
	}
	h.Exec(`UPDATE subs_links SET state = 'deleted', deleted_at = ? WHERE name = 'Mom'`, at(10*day))
	if err := h.Mod.Subs.Delete(bg, id, "admin"); !errors.Is(err, subs.ErrHasLinks) {
		t.Fatalf("with a link deleted 10 days ago: %v", err)
	}
	holders, err := h.Mod.Subs.Holders(bg, id)
	if err != nil || len(holders) != 1 || !holders[0].Ends.Equal(h.Now.Add(20*day)) {
		t.Fatalf("holders: %+v %v", holders, err)
	}
	if got := h.App.I18n.Localizer(i18n.EN, h.App.Cfg.TZ).Date(holders[0].Ends); got != "27 Oct 2026" {
		t.Errorf("tombstone ends %s", got)
	}
	// a shorter tombstone period frees it
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.tombstone": "168h0m0s"}); err != nil {
		t.Fatal(err)
	}
	if holders, _ := h.Mod.Subs.Holders(bg, id); len(holders) != 0 {
		t.Errorf("with a 7-day tombstone: %+v", holders)
	}
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.tombstone": "720h0m0s"}); err != nil {
		t.Fatal(err)
	}
	h.Exec(`UPDATE subs_links SET deleted_at = ? WHERE name = 'Mom'`, at(31*day))
	if err := h.Mod.Subs.Delete(bg, id, "admin"); err != nil {
		t.Fatalf("with only a link deleted 31 days ago: %v", err)
	}
	if ev := h.Events("subscription.deleted"); len(ev) != 1 || ev[0].Payload["name"] != "Family" {
		t.Errorf("deleted: %+v", ev)
	}
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM subs_links WHERE name = 'Mom' AND subscription_id IS NULL`); err != nil || n != 1 {
		t.Errorf("the old link: %d %v", n, err)
	}
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM subs_subscription_servers`); err != nil || n != 0 {
		t.Errorf("members left: %d", n)
	}
	if _, err := h.Mod.Subs.Get(bg, id); !errors.Is(err, subs.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

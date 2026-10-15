package pages_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestNotificationsPageTogglesRules(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	page := l.Get("/settings/notifications").Body.String()
	for _, want := range []string{"job.failed", "backup.failed", `aria-checked="true"`, "default: on", "There is no Telegram bot yet", "/settings/integrations/telegram"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "notification.failed") {
		t.Error("notification.failed can be toggled")
	}

	// each module's rules are a group that closes (a phone starts with them closed)
	if !strings.Contains(page, `<details class="rules" open`) || !strings.Contains(page, `<summary class="pc-title"><h2>`) || !strings.Contains(page, ` events`) {
		t.Errorf("the rules aren't in groups that close:\n%s", page)
	}
	rec := l.Post("/settings/notifications/job.failed", url.Values{"enabled": {"0"}})
	if rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	// back at the rule, whose group stays open
	if loc := rec.Header().Get("Location"); loc != "/settings/notifications#rule-job.failed" {
		t.Errorf("redirected to %q", loc)
	}
	if !strings.Contains(l.Get("/settings/notifications").Body.String(), `id="rule-job.failed"`) || !strings.Contains(l.Get("/settings/notifications").Body.String(), "1 changed") {
		t.Error("the rule has no anchor or its group doesn't count it as changed")
	}
	rules, err := s.App.Notify.Rules(t.Context(), 1, access.PresetOwner.Grant())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.Type.Name == "job.failed" && (r.Enabled || !r.Default) {
			t.Errorf("%+v", r)
		}
	}
	changed := l.Get("/settings/notifications?show=changed").Body.String()
	if !strings.Contains(changed, "job.failed") || strings.Contains(changed, "backup.failed") || !strings.Contains(changed, "changed") {
		t.Errorf("the changed filter:\n%s", changed)
	}
	if rec := l.Post("/settings/notifications/no.such_type", url.Values{"enabled": {"1"}}); rec.Code != 404 {
		t.Errorf("unknown type: %d", rec.Code)
	}
	if rec := l.Post("/settings/notifications/job.failed", url.Values{"enabled": {"maybe"}}); rec.Code != 400 {
		t.Errorf("bad value: %d", rec.Code)
	}
}

// A member's page lists only the rules of what they can be told about, and
// what they turn off is theirs alone.
func TestNotificationRulesArePerMember(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	anna := s.Join("anna", access.Grant{}.With(access.Subscriptions, access.LevelOwn), "198.51.100.2:1")
	page := anna.Get("/settings/notifications").Body.String()
	for _, want := range []string{`id="rule-auth.signed_in"`, `id="rule-auth.password_changed"`} {
		if !strings.Contains(page, want) {
			t.Errorf("anna's page lacks %s", want)
		}
	}
	for _, hidden := range []string{`id="rule-job.failed"`, `id="rule-member.added"`, `id="rule-backup.failed"`} {
		if strings.Contains(page, hidden) {
			t.Errorf("anna's page offers %s", hidden)
		}
	}
	if rec := anna.Post("/settings/notifications/job.failed", url.Values{"enabled": {"0"}}); rec.Code != 404 {
		t.Errorf("anna turned off a rule out of her reach: %d", rec.Code)
	}
	if rec := anna.Post("/settings/notifications/auth.password_changed", url.Values{"enabled": {"0"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	if strings.Contains(l.Get("/settings/notifications").Body.String(), "1 changed") {
		t.Error("anna's change shows on member 1's page")
	}
	if !strings.Contains(anna.Get("/settings/notifications").Body.String(), "1 changed") {
		t.Error("anna's change doesn't show on her page")
	}
}

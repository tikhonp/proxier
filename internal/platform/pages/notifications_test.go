package pages_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestNotificationsPageTogglesRules(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	page := l.Get("/settings/notifications").Body.String()
	for _, want := range []string{"job.failed", "backup.failed", `aria-checked="true"`, "default: on", "Telegram is not configured"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "notification.failed") {
		t.Error("notification.failed can be toggled")
	}

	if rec := l.Post("/settings/notifications/job.failed", url.Values{"enabled": {"0"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	rules, err := s.App.Notify.Rules(t.Context())
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

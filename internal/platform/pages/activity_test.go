package pages_test

import (
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func record(t *testing.T, s *sitetest.Site, typ string, subj events.Subject, actor string, payload map[string]any) {
	t.Helper()
	err := s.App.DB.Write(t.Context(), func(tx *sqlx.Tx) error {
		_, err := s.App.Events.Record(t.Context(), tx, events.Event{Type: typ, Subject: subj, Actor: actor, Payload: payload})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestActivitySentences(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	record(t, s, "job.failed", events.Subject{Type: "job", ID: "7"}, "job:7", map[string]any{"error": "boom"})
	record(t, s, "settings.changed", events.Subject{Type: "mystery", ID: "9"}, "admin", nil)
	body := l.Get("/activity").Body.String()
	if !strings.Contains(body, `href="/jobs/7"`) || !strings.Contains(body, "#7") || !strings.Contains(body, "failed: boom") {
		t.Fatalf("named, linked subject missing:\n%s", body)
	}
	if !strings.Contains(body, "mystery:9") {
		t.Fatalf("an unknown subject should fall back to type:id:\n%s", body)
	}
	if !strings.Contains(body, "job #7") {
		t.Fatalf("job actor:\n%s", body)
	}

	// the sign-in's subject is in its sentence, which has a verb in Russian
	// too, not "Вход с адреса ::1 администратор"
	record(t, s, "auth.signed_in", events.Subject{Type: "member", ID: "1"}, events.MemberActor(1), map[string]any{"ip": "::1"})
	body = strings.Join(strings.Fields(l.Get("/activity").Body.String()), " ")
	if !strings.Contains(body, "Signed in as") || !strings.Contains(body, `class="text">admin</a> from ::1`) {
		t.Errorf("sign-in:\n%s", body)
	}
}

// Before members, the one admin acted as "admin" on the subject admin:1:
// those rows name member 1 as member:1 rows do.
func TestActivityNamesMembersAndTheOldAdmin(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	if _, err := s.App.DB.W.Exec(`UPDATE members SET username = 'tikhon' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.App.DB.W.Exec(`DELETE FROM events`); err != nil {
		t.Fatal(err)
	}
	record(t, s, "auth.password_changed", events.Subject{Type: "admin", ID: "1"}, "admin", map[string]any{"via": "ui"})
	record(t, s, "auth.totp_enabled", events.Subject{Type: "member", ID: "1"}, events.MemberActor(1), nil)
	body := strings.Join(strings.Fields(l.Get("/activity").Body.String()), " ")
	for _, want := range []string{
		`<span class="">tikhon</span> <span class="subtle">Changed the password of <a href="/settings/security" class="text">tikhon</a>`,
		`<span class="">tikhon</span> <span class="subtle">Turned on the authenticator app of <a href="/settings/security" class="text">tikhon</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "member:1") || strings.Contains(body, `<span class="">admin</span>`) {
		t.Errorf("an actor shown as stored:\n%s", body)
	}
}

func TestActivityFilters(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	for i := 0; i < 55; i++ {
		record(t, s, "backup.completed", events.Subject{}, "system", nil)
	}
	record(t, s, "backup.failed", events.Subject{}, "system", map[string]any{"error": "disk full"})

	body := l.Get("/activity?type=backup.failed").Body.String()
	if !strings.Contains(body, "disk full") || strings.Contains(body, "Wrote a database backup") {
		t.Fatalf("type filter:\n%s", body)
	}
	all := l.Get("/activity").Body.String()
	if strings.Count(all, `class="row actrow`) != 50 || !strings.Contains(all, "before=") {
		t.Fatalf("first page should hold 50 rows and link to older ones: %d", strings.Count(all, `class="row actrow`))
	}
	if !strings.Contains(l.Get("/activity?module=platform&range=24h").Body.String(), "disk full") {
		t.Fatal("module and range filters")
	}
	if strings.Contains(l.Get("/activity?module=nothing").Body.String(), "disk full") {
		t.Fatal("module filter should exclude")
	}
}

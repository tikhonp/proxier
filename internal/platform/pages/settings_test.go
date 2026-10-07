package pages_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestPostWithoutCSRFTokenIsRefused(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/settings/general", Cookies: []*http.Cookie{l.Cookie},
		Form: url.Values{"general.instance_name": {"Changed"}}})
	if rec.Code != 403 {
		t.Fatalf("status %d, want 403", rec.Code)
	}
	if got, _ := s.App.Settings.Get(t.Context(), "general.instance_name"); got != "Proxier" {
		t.Errorf("instance name changed to %q", got)
	}
	// and with the token it saves
	if rec := l.Post("/settings/general", url.Values{"general.instance_name": {"Changed"}}); rec.Code != 303 {
		t.Fatalf("with token: %d", rec.Code)
	}
	if got, _ := s.App.Settings.Get(t.Context(), "general.instance_name"); got != "Changed" {
		t.Errorf("instance name %q", got)
	}
}

func TestGeneralSettingsShowFieldErrors(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	rec := l.Post("/settings/general", url.Values{"general.time_zone": {"Mars/Olympus"}, "general.instance_name": {"Other"}})
	if rec.Code != 422 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "unknown time zone") || !strings.Contains(body, "Mars/Olympus") {
		t.Errorf("the message and the typed value should show:\n%s", body)
	}
	if got, _ := s.App.Settings.Get(t.Context(), "general.instance_name"); got != "Proxier" {
		t.Errorf("a refused save wrote %q", got)
	}
	if got, _ := s.App.Settings.Get(t.Context(), "general.time_zone"); got == "Mars/Olympus" {
		t.Error("the bad zone was saved")
	}
}

func TestSecurityPageListsAndEndsSessions(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	laptop := s.SignIn("")
	phone := s.SignIn("198.51.100.4:1")

	page := laptop.Get("/settings/security").Body.String()
	if !strings.Contains(page, "Chrome on macOS") || !strings.Contains(page, "this one") || !strings.Contains(page, "2 open") {
		t.Errorf("sessions list:\n%s", page)
	}
	// end the phone from the laptop
	rec := laptop.Post("/settings/security/sessions/"+itoa(phone.ID)+"/sign-out", nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/security" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := phone.Get("/"); rec.Code != 303 {
		t.Errorf("the phone's session should be over: %d", rec.Code)
	}
	if !strings.Contains(laptop.Get("/settings/security").Body.String(), "signed out") {
		t.Error("the ended session should be listed under recently ended")
	}
	// ending the current one goes to /login
	rec = laptop.Post("/settings/security/sessions/"+itoa(laptop.ID)+"/sign-out", nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatalf("ending this session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSignOutEverywhereDialogAndEffect(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	a := s.SignIn("")
	b := s.SignIn("198.51.100.4:1")
	page := a.Get("/settings/security").Body.String()
	if !strings.Contains(page, "2 sessions, including this one, will end.") {
		t.Errorf("the dialog should name the count:\n%s", page)
	}
	rec := a.Post("/settings/security/sessions/sign-out-everywhere", nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	if a.Get("/").Code != 303 || b.Get("/").Code != 303 {
		t.Error("every session should be over")
	}
}

func TestChangePasswordThroughTheUI(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	laptop := s.SignIn("")
	phone := s.SignIn("198.51.100.4:1")

	for name, f := range map[string]url.Values{
		"mismatch": {"old": {sitetest.Password}, "new": {"a brand new password"}, "again": {"a different one!!"}},
		"wrong":    {"old": {"wrong wrong wrong"}, "new": {"a brand new password"}, "again": {"a brand new password"}},
		"short":    {"old": {sitetest.Password}, "new": {"short"}, "again": {"short"}},
	} {
		if rec := laptop.Post("/settings/security/password", f); rec.Code != 422 {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if phone.Get("/").Code != 200 {
		t.Fatal("refused changes must not end sessions")
	}
	rec := laptop.Post("/settings/security/password", url.Values{"old": {sitetest.Password}, "new": {"a brand new password"}, "again": {"a brand new password"}})
	if rec.Code != 303 {
		t.Fatalf("status %d", rec.Code)
	}
	if phone.Get("/").Code != 303 {
		t.Error("the phone's session should end")
	}
	if laptop.Get("/").Code != 200 {
		t.Error("the laptop's session should stay")
	}
}

func TestLockoutSettingsSave(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	if rec := l.Post("/settings/security/lockout", url.Values{"security.lockout_failures": {"2"}}); rec.Code != 422 || !strings.Contains(rec.Body.String(), `value="2"`) {
		t.Fatalf("out of bounds: %d", rec.Code)
	}
	if rec := l.Post("/settings/security/lockout", url.Values{"security.lockout_failures": {"3"}, "security.lockout_window": {"10m"}}); rec.Code != 303 {
		t.Fatalf("save: %d", rec.Code)
	}
	if n, _ := s.App.Settings.GetInt(t.Context(), "security.lockout_failures"); n != 3 {
		t.Errorf("failures = %d", n)
	}
}

func TestLastSeenShownInRelativeTime(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	s.Advance(5 * time.Minute)
	if !strings.Contains(l.Get("/settings/security").Body.String(), "ago") && !strings.Contains(l.Get("/settings/security").Body.String(), "just now") {
		t.Error("relative times missing")
	}
}

func itoa(n int64) string { return fmtInt(n) }

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

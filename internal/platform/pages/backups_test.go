package pages_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestDownloadLatest(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")

	if rec := l.Get("/settings/backups/latest"); rec.Code != 404 {
		t.Fatalf("with no backup: %d", rec.Code)
	}
	if rec := s.Do(sitetest.Req{Path: "/settings/backups/latest"}); rec.Code != 303 && rec.Code != 401 && rec.Code != 302 {
		t.Fatalf("signed out: %d", rec.Code)
	}

	dir := s.App.Backup.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"proxier-2026-10-05.db":      "older",
		"proxier-2026-10-06.db":      "the newest snapshot",
		".proxier-2026-10-07.db.tmp": "half written",
		"proxier-2026-10-07.db.tmp":  "half written too",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := l.Get("/settings/backups/latest")
	if rec.Code != 200 || rec.Body.String() != "the newest snapshot" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="proxier-2026-10-06.db"` {
		t.Errorf("Content-Disposition %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control %q", got)
	}
}

func TestBackupsPageAndBackUpNow(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	if body := l.Get("/settings/backups").Body.String(); !strings.Contains(body, "No backups yet.") || !strings.Contains(body, "Back up now") ||
		strings.Contains(body, "Download latest") {
		t.Fatalf("empty page:\n%s", body)
	}

	rec := l.Post("/settings/backups/run", nil)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.HasPrefix(loc, "/jobs/") {
		t.Fatalf("Back up now: %d %s", rec.Code, loc)
	}
	// A second press while the first waits is the same job.
	if again := l.Post("/settings/backups/run", nil).Header().Get("Location"); again != loc {
		t.Fatalf("second press made %s, not %s", again, loc)
	}
	if body := l.Get(loc).Body.String(); !strings.Contains(body, "Database backup") {
		t.Errorf("the job page:\n%s", body)
	}

	// The schedule form validates the time.
	if rec := l.Post("/settings/backups", map[string][]string{"backups.time": {"25:99"}, "backups.keep": {"14"}}); rec.Code != 422 {
		t.Fatalf("a bad time: %d", rec.Code)
	}
	if rec := l.Post("/settings/backups", map[string][]string{"backups.time": {"02:15"}, "backups.keep": {"7"}}); rec.Code != 303 {
		t.Fatalf("a good schedule: %d", rec.Code)
	}
	if body := l.Get("/settings/backups?saved=1").Body.String(); !strings.Contains(body, `value="02:15"`) {
		t.Errorf("the saved time is not on the page:\n%s", body)
	}
}

func TestTailnetPageWhileOff(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	body := l.Get("/settings/integrations/tailnet").Body.String()
	for _, want := range []string{"Tailnet", "off", "Pre-auth key", "Re-authenticate"} {
		if !strings.Contains(body, want) {
			t.Errorf("tailnet page lacks %q", want)
		}
	}
	if list := l.Get("/settings/integrations").Body.String(); !strings.Contains(list, "/settings/integrations/tailnet") {
		t.Errorf("the integrations list has no Tailnet row:\n%s", list)
	}
	if rec := l.Post("/settings/integrations/tailnet/reauth", map[string][]string{"authkey": {"  "}}); rec.Code != 422 ||
		!strings.Contains(rec.Body.String(), "Paste a pre-auth key.") {
		t.Fatalf("an empty key: %d", rec.Code)
	}
}

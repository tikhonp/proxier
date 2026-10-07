package servers_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func site(t *testing.T) *sitetest.Site {
	t.Helper()
	return sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
}

func TestModuleIsWired(t *testing.T) {
	s := site(t)
	ctx := context.Background()
	app := s.App

	if _, ok := app.Module("servers"); !ok {
		t.Fatal("the module is not registered")
	}
	// events: declared, with their sentence; the notifying ones with a text
	n := 0
	for _, e := range app.Events.Types() {
		if e.Module != "servers" {
			continue
		}
		n++
		if !app.I18n.Has("event." + e.Name) {
			t.Errorf("%s has no event.%s", e.Name, e.Name)
		}
		if (e.Notify || e.NotifyIf != nil) && !app.I18n.Has("notify."+e.Name) {
			t.Errorf("%s notifies but has no notify.%s", e.Name, e.Name)
		}
	}
	if n != len(servers.Events) || n < 30 {
		t.Errorf("%d events declared, module has %d", n, len(servers.Events))
	}
	// the notification rules the docs fix
	notifying := map[string]bool{}
	for _, e := range servers.Events {
		if e.Notify {
			notifying[e.Name] = true
		}
	}
	for _, name := range []string{"template.agent_session_opened", "server.provisioning_failed", "server.activated", "server.redeploy_failed",
		"server.credentials_rotated", "server.health_changed", "server.still_unhealthy", "server.cert_expiring", "server.disk_low",
		"health.foreign_unreachable", "health.home_recovered"} {
		if !notifying[name] {
			t.Errorf("%s must notify by default", name)
		}
		delete(notifying, name)
	}
	if len(notifying) != 0 {
		t.Errorf("notifying by default and not in events.md: %v", notifying)
	}

	// the health rule: recovery notifies only from blocked or down
	rule := map[string]bool{}
	for _, e := range servers.Events {
		if e.Name == "server.health_changed" {
			for _, c := range []struct{ from, to string }{
				{"healthy", "blocked"}, {"healthy", "down"}, {"blocked", "healthy"}, {"down", "healthy"},
				{"healthy", "degraded"}, {"degraded", "healthy"}, {"healthy", "unknown"}, {"unknown", "healthy"}, {"down", "degraded"},
			} {
				rule[c.from+">"+c.to] = e.NotifyIf(map[string]any{"from": c.from, "to": c.to})
			}
		}
	}
	for k, want := range map[string]bool{"healthy>blocked": true, "healthy>down": true, "blocked>healthy": true, "down>healthy": true,
		"healthy>degraded": false, "degraded>healthy": false, "healthy>unknown": false, "unknown>healthy": false, "down>degraded": false} {
		if rule[k] != want {
			t.Errorf("health change %s notifies=%v, want %v", k, rule[k], want)
		}
	}
	// failures notify unless the job was cancelled
	for _, e := range servers.Events {
		if e.Name == "server.provisioning_failed" || e.Name == "server.redeploy_failed" {
			if !e.NotifyIf(map[string]any{"step": "x"}) || e.NotifyIf(map[string]any{"cancelled": true}) {
				t.Errorf("%s: a cancelled one must not notify", e.Name)
			}
		}
	}

	// settings: the section, its default, and the validation of the pattern
	v, err := app.Settings.Lookup(ctx, "servers.hostname_pattern")
	if err != nil || v.Value != "{location}-{number}.hosts.tikhonnnnn.com" || !v.IsDefault {
		t.Fatalf("setting: %+v %v", v, err)
	}
	for _, bad := range []string{"{location}.hosts.example.com", "x-{number}.example.com", "{location}-{number}", "{location}_{number}.example.com"} {
		err := app.Settings.Set(ctx, "admin", "servers", map[string]string{"servers.hostname_pattern": bad})
		if _, ok := err.(settings.FieldErrors); !ok {
			t.Errorf("pattern %q must be refused, got %v", bad, err)
		}
	}
	if err := app.Settings.Set(ctx, "admin", "servers", map[string]string{"servers.hostname_pattern": "{location}{number}.px.example.org"}); err != nil {
		t.Errorf("a valid pattern: %v", err)
	}
	if !app.I18n.Has("settings.field.servers.hostname_pattern") || !app.I18n.Has("settings.servers") {
		t.Error("settings texts are missing")
	}

	// nav and the page behind it
	l := s.SignIn("")
	if body := l.Get("/").Body.String(); !strings.Contains(body, `href="/locations"`) {
		t.Error("the nav has no Locations")
	}
	if rec := l.Get("/locations"); rec.Code != 200 {
		t.Errorf("/locations: %d", rec.Code)
	}
}

func TestSubjectsAreNamedAndSearched(t *testing.T) {
	s := site(t)
	ctx := context.Background()
	m, _ := s.App.Module("servers")
	mod := m.(*servers.Module)
	id, err := mod.Store.CreateLocation(ctx, "nl", "Netherlands", "NL", "admin")
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := mod.Templates.Create(ctx, "My stack", "my-stack", "", "admin")
	idStr := func(n int64) string { return strings.TrimSpace(strings.Repeat(" ", 0) + itoa(n)) }

	got, err := mod.NameSubjects(ctx, "location", []string{idStr(id), "999", "x"})
	if err != nil || len(got) != 1 || got[idStr(id)].Label != "nl · Netherlands" || got[idStr(id)].Href != "/locations" {
		t.Errorf("location: %+v %v", got, err)
	}
	got, _ = mod.NameSubjects(ctx, "template", []string{idStr(tpl)})
	if got[idStr(tpl)].Label != "My stack" || got[idStr(tpl)].Href != "/templates/"+idStr(tpl) {
		t.Errorf("template: %+v", got)
	}
	ctx = i18n.WithLocalizer(ctx, s.App.I18n.Localizer(i18n.EN, time.UTC))
	got, _ = mod.NameSubjects(ctx, "home", []string{"1"})
	if got["1"].Label != "Home internet" || got["1"].Href != "/" {
		t.Errorf("home: %+v", got)
	}
	// a server: flag, name, link
	if _, err := s.App.DB.W.ExecContext(ctx, `INSERT INTO servers_servers (id, location_id, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at)
		VALUES (7, ?, 1, 'nl-1', '203.0.113.7', 'h', 'h', 'provisioning', ?, 1, '2026-10-07T00:00:00.000Z')`, id, tpl); err != nil {
		t.Fatal(err)
	}
	got, _ = mod.NameSubjects(ctx, "server", []string{"7"})
	if got["7"].Label != "🇳🇱 nl-1" || got["7"].Href != "/servers/7" {
		t.Errorf("server: %+v", got)
	}

	hits, err := mod.Search(ctx, "neth", 5)
	if err != nil || len(hits) != 1 || hits[0].Href != "/locations" || !strings.Contains(hits[0].Label, "Netherlands") {
		t.Errorf("search: %+v %v", hits, err)
	}
	if hits, _ := mod.Search(ctx, "zzz", 5); len(hits) != 0 {
		t.Errorf("search for nothing: %+v", hits)
	}
	// templates are found by name or slug, and link to their page
	hits, err = mod.Search(ctx, "my-stack", 5)
	if err != nil || len(hits) != 1 || hits[0].Href != "/templates/"+idStr(tpl) || !strings.Contains(hits[0].Label, "My stack") || hits[0].Meta != "Template" {
		t.Errorf("template search: %+v %v", hits, err)
	}
	if hits, _ = mod.Search(ctx, "stack", 5); len(hits) != 1 {
		t.Errorf("template search by name: %+v", hits)
	}
}

func itoa(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

var keyLiteral = regexp.MustCompile(`"((?:servers|templates|locations|health|stats|agent)\.[a-z0-9_.]*[a-z0-9_])"(\s*\+)?`)

// TestEveryUsedMessageKeyExists scans the module's Go and templ files for
// literal keys, as the platform's test does for its own.
func TestEveryUsedMessageKeyExists(t *testing.T) {
	s := site(t)
	var files []string
	_ = filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".templ")) &&
			!strings.HasSuffix(p, "_templ.go") && !strings.HasSuffix(p, "_test.go") && !strings.Contains(p, "testdata") &&
			(p == "servers.go" || strings.HasPrefix(p, "pages/")) { // events.go holds event type names, which look like keys
			files = append(files, p)
		}
		return nil
	})
	used := 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, m := range keyLiteral.FindAllStringSubmatch(line, -1) {
				if m[2] != "" || strings.HasPrefix(m[1], "servers.hostname") { // "prefix." + variable, a setting
					continue
				}
				used++
				if !s.App.I18n.Has(m[1]) {
					t.Errorf("%s:%d: message key %q is not in the catalog", f, i+1, m[1])
				}
			}
		}
	}
	if used < 20 {
		t.Errorf("only %d keys found; the scan is broken", used)
	}
	for _, k := range []string{"saved.created", "saved.updated", "saved.deleted"} {
		if !s.App.I18n.Has("locations." + k) {
			t.Errorf("locations.%s is missing", k)
		}
	}
}

package subscriptions_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

func TestModuleIsWired(t *testing.T) {
	h := substest.New(t)
	app := h.App
	if _, ok := app.Module("subscriptions"); !ok {
		t.Fatal("the module is not registered")
	}
	n := 0
	notifying := map[string]bool{}
	for _, e := range app.Events.Types() {
		if e.Module != "subscriptions" {
			continue
		}
		n++
		if !app.I18n.Has("event." + e.Name) {
			t.Errorf("%s has no event.%s", e.Name, e.Name)
		}
		if e.Notify || e.NotifyIf != nil {
			notifying[e.Name] = true
			if !app.I18n.Has("notify."+e.Name) || !app.I18n.Has("notify."+e.Name+".body") {
				t.Errorf("%s notifies but has no notify.%s (+ .body)", e.Name, e.Name)
			}
			if e.Emoji == "" {
				t.Errorf("%s notifies without an emoji", e.Name)
			}
		}
	}
	if n != len(subscriptions.Events) || n != 17 {
		t.Errorf("%d events declared, module has %d", n, len(subscriptions.Events))
	}
	for _, name := range []string{"subscription.all_unhealthy", "link.expiring_soon", "link.expired", "link.shared_suspected"} {
		if !notifying[name] {
			t.Errorf("%s must notify by default", name)
		}
		delete(notifying, name)
	}
	if len(notifying) != 0 {
		t.Errorf("notifying by default and not in events.md: %v", notifying)
	}

	// the settings section: every key with its default, texts, bounds
	ctx := context.Background()
	for key, want := range map[string]string{
		"subscriptions.link_language": "ru", "subscriptions.expiry_warning": "72h0m0s", "subscriptions.alert_networks": "4",
		"subscriptions.alert_apps": "3", "subscriptions.network_country_url": "https://ipinfo.io/{ip}/country",
		"subscriptions.tombstone": "720h0m0s", "subscriptions.fetch_retention": "2160h0m0s",
	} {
		v, err := app.Settings.Lookup(ctx, key)
		if err != nil || v.Value != want || !v.IsDefault {
			t.Errorf("%s: %+v %v", key, v, err)
		}
		name := strings.TrimPrefix(key, "subscriptions.")
		if !app.I18n.Has("settings.field.subscriptions."+name) || !app.I18n.Has("settings.field.subscriptions."+name+".help") {
			t.Errorf("%s has no texts", key)
		}
	}
	if !app.I18n.Has("settings.subscriptions") {
		t.Error("settings.subscriptions is missing")
	}
	for key, bad := range map[string]string{
		"subscriptions.fetch_retention": "24h0m0s", "subscriptions.tombstone": "1h0m0s", "subscriptions.alert_apps": "0",
		"subscriptions.network_country_url": "https://ipinfo.io/country", "subscriptions.link_language": "de",
	} {
		if _, ok := app.Settings.Set(ctx, "admin", "subscriptions", map[string]string{key: bad}).(settings.FieldErrors); !ok {
			t.Errorf("%s=%s must be refused", key, bad)
		}
	}
	if err := app.Settings.Set(ctx, "admin", "subscriptions", map[string]string{"subscriptions.network_country_url": ""}); err != nil {
		t.Errorf("an empty lookup URL turns it off: %v", err)
	}

	// nav: Subscriptions with g u
	body := h.Login.Get("/").Body.String()
	if !strings.Contains(body, `href="/subscriptions"`) || !strings.Contains(body, "Subscriptions") {
		t.Error("the nav has no Subscriptions")
	}
	if rec := h.Login.Get("/subscriptions"); rec.Code != 200 {
		t.Errorf("/subscriptions: %d", rec.Code)
	}
	var goKey bool
	for _, it := range h.Mod.Nav() {
		goKey = goKey || it.Href == "/subscriptions" && it.GoKey == "u" && it.Group == "subscriptions"
	}
	if !goKey {
		t.Error("Subscriptions is not g u")
	}
}

func TestSubjectsAreNamedAndSearched(t *testing.T) {
	h := substest.New(t)
	ctx := context.Background()
	id := h.Subscription("Family", 1, 2)
	if _, err := h.Mod.Subs.Create(ctx, "Friends", "Друзья", "", "admin"); err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(id, 10)
	got, err := h.Mod.NameSubjects(ctx, "subscription", []string{sid, "999", "x"})
	if err != nil || len(got) != 1 || got[sid].Label != "Family" || got[sid].Href != "/subscriptions/"+sid {
		t.Fatalf("names: %+v %v", got, err)
	}
	if got, _ := h.Mod.NameSubjects(ctx, "server", []string{sid}); len(got) != 0 {
		t.Errorf("another type: %+v", got)
	}
	ctx = i18n.WithLocalizer(ctx, h.App.I18n.Localizer(i18n.EN, nil))
	hits, err := h.Mod.Search(ctx, "fam", 5)
	if err != nil || len(hits) != 1 || hits[0].Label != "Family" || hits[0].Href != "/subscriptions/1" || hits[0].Meta != "subscription · 2 servers" {
		t.Errorf("by name: %+v %v", hits, err)
	}
	if hits, _ := h.Mod.Search(ctx, "друз", 5); len(hits) != 1 || hits[0].Label != "Friends" {
		t.Errorf("by title: %+v", hits)
	}
	if hits, _ := h.Mod.Search(ctx, "zzz", 5); len(hits) != 0 {
		t.Errorf("nothing: %+v", hits)
	}
	// Activity names the subject
	body := h.Login.Get("/activity?subject=subscription:" + sid).Body.String()
	if !strings.Contains(body, `href="/subscriptions/1"`) {
		t.Error("Activity does not link the subscription")
	}
}

var keyLiteral = regexp.MustCompile(`"((?:subs|links|alerts|cutoff)\.[a-z0-9_.]*[a-z0-9_])"(\s*\+)?`)

// TestEveryUsedMessageKeyExists scans the module's Go and templ files for
// literal keys, as the servers module does.
func TestEveryUsedMessageKeyExists(t *testing.T) {
	h := substest.New(t)
	var files []string
	_ = filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".templ")) &&
			!strings.HasSuffix(p, "_templ.go") && !strings.HasSuffix(p, "_test.go") &&
			p != "events.go" && p != "messages.go" && !strings.HasPrefix(p, "substest/") && !strings.HasPrefix(p, "migrations/") {
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
				if m[2] != "" || strings.HasPrefix(m[1], "subscriptions.") {
					continue
				}
				used++
				if !h.App.I18n.Has(m[1]) {
					t.Errorf("%s:%d: message key %q is not in the catalog", f, i+1, m[1])
				}
			}
		}
	}
	if used < 40 {
		t.Errorf("only %d keys found; the scan is broken", used)
	}
	// keys built from a prefix and a value
	for _, st := range []string{"healthy", "degraded", "blocked", "down", "unknown", "paused"} {
		if !h.App.I18n.Has("subs.health."+st) || !h.App.I18n.Has("subs.state."+st) {
			t.Errorf("no words for %s", st)
		}
	}
}

// TestImportsOnlyServersPorts: the module knows the servers module only by its
// ports (the root package) and the endpoint values (build README, Phase 2).
func TestImportsOnlyServersPorts(t *testing.T) {
	const servers = "github.com/tikhonp/proxier/internal/modules/servers"
	allowed := map[string]bool{servers: true, servers + "/endpoint": true}
	fset := token.NewFileSet()
	n := 0
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.HasPrefix(p, "substest/") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		n++
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if (path == servers || strings.HasPrefix(path, servers+"/")) && !allowed[path] {
				t.Errorf("%s imports %s", p, path)
			}
		}
		return nil
	})
	if err != nil || n < 10 {
		t.Fatalf("%d files: %v", n, err)
	}
}

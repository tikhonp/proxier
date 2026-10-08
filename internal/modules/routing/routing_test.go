package routing_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

func TestModuleIsWired(t *testing.T) {
	h := routingtest.New(t)
	app := h.App
	if _, ok := app.Module("routing"); !ok {
		t.Fatal("the module is not registered")
	}
	n := 0
	notifying := map[string]bool{}
	for _, e := range app.Events.Types() {
		if e.Module != "routing" {
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
	if n != len(routing.Events) || n != 31 {
		t.Errorf("%d events declared, module has %d", n, len(routing.Events))
	}
	for _, name := range []string{"routing.snapshot_rejected", "routing.refresh_failing", "routing.refresh_digest", "routing.catalog_refresh_failed",
		"routing.router_sync_failed", "routing.router_recovered", "routing.drift_detected", "routing.unmanaged_tags_found"} {
		if !notifying[name] {
			t.Errorf("%s must notify by default", name)
		}
		delete(notifying, name)
	}
	if len(notifying) != 0 {
		t.Errorf("notifying by default and not in the contract: %v", notifying)
	}
	types := map[string]func(map[string]any) bool{}
	for _, e := range routing.Events {
		types[e.Name] = e.NotifyIf
	}
	for name, cases := range map[string][]struct {
		p    map[string]any
		want bool
	}{
		"routing.snapshot_rejected":  {{map[string]any{"in_round": true}, false}, {map[string]any{"in_round": false}, true}},
		"routing.refresh_digest":     {{map[string]any{"changed": 0.0, "rejected": 0.0, "failing": 0.0}, false}, {map[string]any{"failing": 1.0}, true}, {map[string]any{"changed": 2}, true}},
		"routing.router_sync_failed": {{map[string]any{"final": false}, false}, {map[string]any{"final": true}, true}},
		"routing.router_recovered":   {{map[string]any{"notified": false}, false}, {map[string]any{"notified": true}, true}},
		"routing.drift_detected":     {{map[string]any{"repair": true}, false}, {map[string]any{"repair": false}, true}},
	} {
		for _, c := range cases {
			if got := types[name](c.p); got != c.want {
				t.Errorf("%s NotifyIf(%v) = %v", name, c.p, got)
			}
		}
	}

	// the settings section: every key with its default, texts, bounds
	ctx := context.Background()
	for key, want := range map[string]string{
		"routing.refresh_at": "04:00", "routing.catalog_at": "04:30", "routing.shrink_min": "20", "routing.shrink_pct": "50",
		"routing.github_token": "", "routing.sync_delay": "30s", "routing.drift_every": "6h0m0s", "routing.drift_repair": "true",
	} {
		v, err := app.Settings.Lookup(ctx, key)
		if err != nil || v.Value != want || !v.IsDefault {
			t.Errorf("%s: %+v %v", key, v, err)
		}
		name := strings.TrimPrefix(key, "routing.")
		if !app.I18n.Has("settings.field.routing."+name) || !app.I18n.Has("settings.field.routing."+name+".help") {
			t.Errorf("%s has no texts", key)
		}
	}
	if !app.I18n.Has("settings.routing") {
		t.Error("settings.routing is missing")
	}
	for key, bad := range map[string]string{
		"routing.refresh_at": "4:00", "routing.catalog_at": "24:00", "routing.shrink_pct": "100", "routing.shrink_min": "0",
		"routing.sync_delay": "11m0s", "routing.drift_every": "30m0s",
	} {
		if _, ok := app.Settings.Set(ctx, "admin", "routing", map[string]string{key: bad}).(settings.FieldErrors); !ok {
			t.Errorf("%s=%s must be refused", key, bad)
		}
	}
	if err := app.Settings.Set(ctx, "admin", "routing", map[string]string{"routing.github_token": "ghp_x", "routing.refresh_at": "23:59"}); err != nil {
		t.Errorf("valid values: %v", err)
	}

	// nav: Lists and Services in the routing group, no go-to key
	nav := h.Mod.Nav()
	if len(nav) != 2 || nav[0].Href != "/routing/lists" || nav[0].Order != 10 || nav[1].Href != "/routing/services" ||
		nav[1].Group != "routing" || nav[1].Order != 20 || nav[0].GoKey != "" || nav[1].GoKey != "" {
		t.Errorf("nav: %+v", nav)
	}
	body := h.Login.Get("/").Body.String()
	if !strings.Contains(body, `href="/routing/services"`) || !strings.Contains(body, `href="/routing/lists"`) {
		t.Error("the nav has no Lists or Services")
	}
	// "Main" exists after the module's migration
	var main string
	if err := app.DB.R.Get(&main, `SELECT name FROM routing_lists WHERE is_default = 1`); err != nil || main != "Main" {
		t.Errorf("Main: %q %v", main, err)
	}
}

func TestSubjectsAreNamedAndSearched(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	h.Up.Site("main", "video", "youtube.com", "youtube.com")
	h.Upstream("anthropic")
	h.Upstream("iplist:youtube.com")
	h.Custom("mine", "example.com")
	got, err := h.Mod.NameSubjects(ctx, "service", []string{"1", "3", "99", "x"})
	if err != nil || len(got) != 2 || got["1"].Label != "anthropic" || got["1"].Href != "/routing/services/1" || got["3"].Label != "mine" {
		t.Errorf("services: %+v %v", got, err)
	}
	got, _ = h.Mod.NameSubjects(ctx, "routing_list", []string{"1", "2"})
	if len(got) != 1 || got["1"].Label != "Main" || got["1"].Href != "/routing/lists/1" {
		t.Errorf("lists: %+v", got)
	}
	ctx = i18n.WithLocalizer(ctx, h.App.I18n.Localizer(i18n.EN, nil))
	got, _ = h.Mod.NameSubjects(ctx, "routing", []string{"refresh", "catalog", "x"})
	if len(got) != 2 || got["refresh"].Label != "Upstream refresh" || got["refresh"].Href != "/routing/services" ||
		got["catalog"].Label != "Catalog" || got["catalog"].Href != "/routing/search" {
		t.Errorf("routing: %+v", got)
	}

	hits, err := h.Mod.Search(ctx, "anthro", 10)
	if err != nil || len(hits) != 1 || hits[0].Label != "anthropic" || hits[0].Meta != "service · v2fly · 2 domains" || hits[0].Href != "/routing/services/1" {
		t.Errorf("by tag: %+v %v", hits, err)
	}
	if hits, _ := h.Mod.Search(ctx, "iplist:you", 10); len(hits) != 1 || hits[0].Label != "youtube.com" || hits[0].Meta != "service · iplist · 1 domain" {
		t.Errorf("by selector: %+v", hits)
	}
	if hits, _ := h.Mod.Search(ctx, "MINE", 10); len(hits) != 1 || hits[0].Label != "mine" {
		t.Errorf("by name: %+v", hits)
	}
	if hits, _ := h.Mod.Search(ctx, "zzz", 10); len(hits) != 0 {
		t.Errorf("nothing: %+v", hits)
	}
	// lists by name
	h.List("Main", 1, 3)
	if hits, _ := h.Mod.Search(ctx, "mai", 10); len(hits) != 1 || hits[0].Label != "Main" || hits[0].Meta != "routing list · 2 services" || hits[0].Href != "/routing/lists/1" {
		t.Errorf("lists: %+v", hits)
	}
	body := h.Login.Get("/activity?subject=service:1").Body.String()
	if !strings.Contains(body, `href="/routing/services/1"`) {
		t.Error("Activity does not link the service")
	}
}

var keyLiteral = regexp.MustCompile(`"((?:routing|services|lists|catalog|refresh|routers|shadowrocket|mtvpn|discovery)\.[a-z0-9_.]*[a-z0-9_])"(\s*\+)?`)

// TestEveryUsedMessageKeyExists scans the module's Go and templ files for
// literal keys, as the other modules do.
func TestEveryUsedMessageKeyExists(t *testing.T) {
	h := routingtest.New(t)
	var files []string
	_ = filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".templ")) &&
			!strings.HasSuffix(p, "_templ.go") && !strings.HasSuffix(p, "_test.go") && p != "events.go" && p != "messages.go" &&
			!strings.HasPrefix(p, "routingtest/") && !strings.HasPrefix(p, "migrations/") && !strings.HasPrefix(p, "sources/sourcestest/") {
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
				// a key built from a prefix, and the settings keys (not messages)
				if m[2] != "" || strings.HasPrefix(m[1], "routing.") && !strings.HasPrefix(m[1], "routing.subject.") {
					continue
				}
				used++
				if !h.App.I18n.Has(m[1]) {
					t.Errorf("%s:%d: message key %q is not in the catalog", f, i+1, m[1])
				}
			}
		}
	}
	if used < 100 {
		t.Errorf("only %d keys found; the scan is broken", used)
	}
	// keys built from a prefix and a value
	var built []string
	for _, s := range []string{"accepted", "superseded", "rejected"} {
		built = append(built, "services.snap."+s)
	}
	for _, s := range []string{"discovery", "import", "router"} {
		built = append(built, "services.custom_from."+s)
	}
	for _, s := range []string{"source", "tag", "domains", "fields"} {
		built = append(built, "services.event.updated."+s)
	}
	for _, s := range []string{"covered", "owned", "guarded", "pinned"} {
		built = append(built, "lists.drop."+s)
	}
	for _, s := range []string{"added", "removed", "reordered", "default", "fields"} {
		built = append(built, "lists.event."+s)
	}
	built = append(built, "lists.target.router", "lists.target.shadowrocket", "lists.err.name", "lists.err.name_taken", "lists.err.description")
	built = append(built, "services.state.ok", "services.kind.site", "services.kind.group",
		"services.paste.ip", "services.paste.invalid", "services.paste.unsupported", "services.skip.unsupported", "services.skip.invalid",
		"services.err.name", "services.err.name_taken", "services.err.tag_empty", "services.err.tag", "services.err.tag_taken",
		"services.err.description", "services.err.note", "services.err.too_many")
	for _, k := range built {
		if !h.App.I18n.Has(k) {
			t.Errorf("%s is missing", k)
		}
	}
}

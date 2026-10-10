package routerscripts_test

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

	"github.com/tikhonp/proxier/internal/modules/routerscripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestModuleIsWired(t *testing.T) {
	h := rscriptstest.New(t)
	app := h.App
	if _, ok := app.Module("routerscripts"); !ok {
		t.Fatal("the module is not registered")
	}
	n := 0
	for _, e := range app.Events.Types() {
		if e.Module != "routerscripts" {
			continue
		}
		n++
		if !app.I18n.Has("event." + e.Name) {
			t.Errorf("%s has no event.%s", e.Name, e.Name)
		}
		if e.Notify || e.NotifyIf != nil {
			if e.Name != "routerscript.fetched" {
				t.Errorf("%s notifies and isn't in docs/events.md", e.Name)
			}
			if !app.I18n.Has("notify."+e.Name) || !app.I18n.Has("notify."+e.Name+".body") || e.Emoji != "📥" {
				t.Errorf("%s notifies without its texts or emoji", e.Name)
			}
		}
	}
	if n != len(routerscripts.Events) || n != 12 {
		t.Errorf("%d events declared, module has %d", n, len(routerscripts.Events))
	}

	nav := h.Mod.Nav()
	if len(nav) != 1 || nav[0].Group != "routerscripts" || nav[0].Href != "/router-scripts" || nav[0].Order != 10 || nav[0].GoKey != "" {
		t.Errorf("nav: %+v", nav)
	}
	if body := h.Login.Get("/").Body.String(); !strings.Contains(body, `href="/router-scripts"`) || !strings.Contains(body, "Router scripts") {
		t.Error("the nav has no Scripts under Router scripts")
	}

	ctx := i18n.WithLocalizer(context.Background(), app.I18n.Localizer(i18n.EN, nil))
	id := h.Script("fresh-router", paramstest.WithEnd(paramstest.Today()))
	h.Exec(`UPDATE rscripts_scripts SET slug = 'fr-boot' WHERE id = ?`, id)
	got, err := h.Mod.NameSubjects(ctx, "router_script", []string{strconv.FormatInt(id, 10), "99", "x"})
	if err != nil || len(got) != 1 || got["1"].Label != "fresh-router" || got["1"].Href != "/router-scripts/1" {
		t.Errorf("subjects: %+v %v", got, err)
	}
	for _, q := range []string{"FRESH", "fr-boot", ""} {
		hits, err := h.Mod.Search(ctx, q, 10)
		if err != nil || len(hits) != 2 || hits[0].Label != "fresh-router" || hits[0].Meta != "router script · v1 · 0 generations" || hits[0].Href != "/router-scripts/1" ||
			hits[1].Label != "Generate for a new router · fresh-router" || hits[1].Href != "/router-scripts/1/generate" {
			t.Errorf("search %q: %+v %v", q, hits, err)
		}
	}
	if hits, _ := h.Mod.Search(ctx, "zzz", 10); len(hits) != 0 {
		t.Errorf("nothing: %+v", hits)
	}
	if err := h.Mod.Scripts.Archive(ctx, id, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if hits, _ := h.Mod.Search(ctx, "fresh", 10); len(hits) != 0 {
		t.Errorf("archived: %+v", hits)
	}
	if body := h.Login.Get("/activity?subject=router_script:1").Body.String(); !strings.Contains(body, `href="/router-scripts/1"`) {
		t.Error("Activity does not link the script")
	}
}

var keyLiteral = regexp.MustCompile(`"((?:rscripts|scripts|params|generations)\.[a-z0-9_.]*[a-z0-9_])"(\s*\+)?`)

// TestEveryUsedMessageKeyExists scans the module's Go and templ files for
// literal keys, as the other modules do.
func TestEveryUsedMessageKeyExists(t *testing.T) {
	h := rscriptstest.New(t)
	var files []string
	_ = filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".templ")) &&
			!strings.HasSuffix(p, "_templ.go") && !strings.HasSuffix(p, "_test.go") && p != "messages.go" && p != "messages_generations.go" && p != "params/messages.go" &&
			!strings.HasPrefix(p, "rscriptstest/") && !strings.HasPrefix(p, "migrations/") {
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
				if m[2] != "" {
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
}

// TestImportsOnlyPorts: the module knows subscriptions and routing only by
// their root packages, and servers not at all (build README, Phase 4).
func TestImportsOnlyPorts(t *testing.T) {
	const mods = "github.com/tikhonp/proxier/internal/modules/"
	fset := token.NewFileSet()
	n := 0
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		n++
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case path == mods+"servers" || strings.HasPrefix(path, mods+"servers/"):
				t.Errorf("%s imports %s", p, path)
			case strings.HasPrefix(p, "rscriptstest/"):
				// the harness wires the real modules as main.go does
			case strings.HasPrefix(path, mods+"subscriptions/"), strings.HasPrefix(path, mods+"routing/"):
				t.Errorf("%s imports %s", p, path)
			}
		}
		return nil
	})
	if err != nil || n < 15 {
		t.Fatalf("%d files: %v", n, err)
	}
}

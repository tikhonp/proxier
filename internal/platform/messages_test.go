package platform_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var (
	keyLiteral = regexp.MustCompile(`"((?:ui|nav|dash|keys|err|settings|auth)\.[a-z0-9_.]*[a-z0-9_])"(\s*\+)?`)
	actionID   = regexp.MustCompile(`\bID:\s*"[^"]*"`)
)

// TestEveryUsedKeyExists scans the platform's Go and templ sources for
// literal message keys and checks each one is in the catalog. Keys built from
// a prefix and a variable are covered by the explicit lists below.
func TestEveryUsedKeyExists(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	cat := s.App.I18n

	var files []string
	for _, dir := range []string{"ui", "pages", "web"} {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".templ")) &&
				!strings.HasSuffix(p, "_templ.go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
	}
	files = append(files, "platform.go")
	if len(files) < 8 {
		t.Fatalf("scanned only %v", files)
	}
	used := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if i := strings.Index(line, " //"); i >= 0 {
				line = line[:i]
			}
			line = actionID.ReplaceAllString(line, "")
			for _, m := range keyLiteral.FindAllStringSubmatch(line, -1) {
				if m[2] != "" { // "prefix." + variable
					continue
				}
				used++
				if !cat.Has(m[1]) {
					t.Errorf("%s:%d: message key %q is not in the catalog", f, i+1, m[1])
				}
			}
		}
	}
	if used < 50 {
		t.Errorf("only %d keys found; the scan is broken", used)
	}

	// keys built at run time
	var dynamic []string
	for _, g := range []string{"overview", "servers", "subscriptions", "routing", "routerscripts", "system"} {
		dynamic = append(dynamic, "nav.group."+g)
	}
	for _, e := range []string{"idle", "absolute"} {
		dynamic = append(dynamic, "auth.ended."+e, "auth.ended."+e+".back")
	}
	for _, r := range []string{"signed_out", "signed_out_everywhere", "password_changed", "password_reset"} {
		dynamic = append(dynamic, "settings.end."+r)
	}
	for _, c := range []string{"403", "404", "500"} {
		dynamic = append(dynamic, "err."+c, "err."+c+".text")
	}
	for _, l := range []string{"en", "ru"} {
		dynamic = append(dynamic, "ui.language."+l, "settings.option.general.language."+l)
	}
	for _, sec := range s.App.Settings.Sections() {
		for _, f := range sec.Fields {
			dynamic = append(dynamic, "settings.field."+f.Key)
		}
	}
	for _, k := range dynamic {
		if !cat.Has(k) {
			t.Errorf("message key %q is not in the catalog", k)
		}
	}
}

func TestEveryEventTypeHasASentence(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	for _, e := range s.App.Events.Types() {
		if !s.App.I18n.Has("event." + e.Name) {
			t.Errorf("event type %q has no event.%s message", e.Name, e.Name)
		}
	}
}

func TestEveryJobTypeHasATitle(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	for _, name := range s.App.Jobs.Types() {
		if !s.App.I18n.Has("job." + name) {
			t.Errorf("job type %q has no job.%s message", name, name)
		}
	}
}

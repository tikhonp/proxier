package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var cancelLink = regexp.MustCompile(`<a [^>]*>\{ i18n\.T\(ctx, "ui\.cancel"\) \}</a>`)

// TestEveryCancelLinkTakesEsc reads every page's source: a form's Cancel link
// is what esc follows (proxier.js), so each one carries data-cancel, except
// one that closes a dialog, which esc closes by itself.
func TestEveryCancelLinkTakesEsc(t *testing.T) {
	var seen int
	err := filepath.WalkDir("../../internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, a := range cancelLink.FindAllString(string(b), -1) {
			seen++
			if !strings.Contains(a, "data-cancel") && !strings.Contains(a, "data-dialog-close") {
				t.Errorf("%s: a Cancel esc doesn't reach: %s", path, a)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 40 {
		t.Fatalf("found %d Cancel links, want every page's (40 or more)", seen)
	}
}

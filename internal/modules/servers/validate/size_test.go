package validate_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
)

func TestSizeLimits(t *testing.T) {
	// 1.2 MiB in one file
	files := seedWith(map[string]string{"site/index.html": string(bytes.Repeat([]byte("a"), 1200*1024))})
	r := run(t, files)
	if !has(r, finding.Error, "size", "site/index.html", 0, "at most 1 MiB") {
		t.Errorf("a 1.2 MiB file must be an error: %v", r.Findings)
	}

	// eleven files of 0.95 MiB: each fine, together over 10 MiB
	files = seedWith(nil)
	for i := range 11 {
		files[fmt.Sprintf("extra%d.bin", i)] = bytes.Repeat([]byte("b"), 950*1024)
	}
	r = run(t, files)
	if !has(r, finding.Error, "size", "", 0, "at most 10 MiB") {
		t.Errorf("a template over 10 MiB must be a global error: %v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Check == "size" && f.Path != "" {
			t.Errorf("no single file is over its limit, got %s", f)
		}
	}
}

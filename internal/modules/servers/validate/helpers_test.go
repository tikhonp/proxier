package validate_test

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/validate"
)

// seedWith returns the seed's tree with the given files replaced (nil deletes).
func seedWith(change map[string]string) map[string][]byte {
	files := seed.Files()
	for p, c := range change {
		if c == "\x00delete" {
			delete(files, p)
			continue
		}
		files[p] = []byte(c)
	}
	return files
}

func run(t *testing.T, files map[string][]byte) finding.Report {
	t.Helper()
	return validate.Validate(context.Background(), validate.Input{Slug: "t", Files: files})
}

// has finds a finding of the severity and check, on the path, whose message
// holds text. line 0 matches any line.
func has(r finding.Report, sev finding.Severity, check, path string, line int, text string) bool {
	for _, f := range r.Findings {
		if f.Severity == sev && f.Check == check && f.Path == path && (line == 0 || f.Line == line) && contains(f.Message, text) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool { return len(sub) == 0 || (len(s) >= len(sub) && index(s, sub) >= 0) }

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

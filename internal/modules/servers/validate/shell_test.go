package validate_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
)

func TestShellSyntaxError(t *testing.T) {
	r := run(t, seedWith(map[string]string{"issue-cert.sh": "#!/bin/bash\nset -e\nif true; then\n  echo hi\n"}))
	if !has(r, finding.Error, "shell", "issue-cert.sh", 0, "") {
		t.Fatalf("an unterminated if must be an error: %v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.Check == "shell" && f.Line == 0 {
			t.Errorf("a shell error must carry its line: %s", f)
		}
	}
}

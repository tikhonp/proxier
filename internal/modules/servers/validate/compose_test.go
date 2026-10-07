package validate_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
)

func TestComposeFindings(t *testing.T) {
	r := run(t, seedWith(nil))
	if !has(r, finding.Warning, "compose", "compose.yaml", 0, "ghcr.io/xtls/xray-core:latest") {
		t.Errorf("no :latest warning for xray: %v", r.Findings)
	}
	// without a tag is the same as latest
	untagged := strings.Replace(string(seedWith(nil)["compose.yaml"]), "nginx:stable-alpine", "nginx", 1)
	r = run(t, seedWith(map[string]string{"compose.yaml": untagged}))
	if !has(r, finding.Warning, "compose", "compose.yaml", 0, "image nginx") {
		t.Errorf("an untagged image must warn: %v", r.Findings)
	}
	// a digest pins
	pinned := strings.Replace(string(seedWith(nil)["compose.yaml"]), "certbot/certbot:latest", "certbot/certbot@sha256:0000000000000000000000000000000000000000000000000000000000000000", 1)
	r = run(t, seedWith(map[string]string{"compose.yaml": pinned}))
	if has(r, finding.Warning, "compose", "compose.yaml", 0, "certbot") {
		t.Errorf("an image pinned by digest must not warn: %v", r.Findings)
	}
	// no services
	r = run(t, seedWith(map[string]string{"compose.yaml": "services: {}\n"}))
	if !has(r, finding.Error, "compose", "compose.yaml", 0, "no services") {
		t.Errorf("a compose file without services must be an error: %v", r.Findings)
	}
	// not compose at all
	r = run(t, seedWith(map[string]string{"compose.yaml": "services:\n  web:\n    image: x\n    bogus_key: 1\n"}))
	if r.OK() {
		t.Errorf("an unknown service key must be an error: %v", r.Findings)
	}
}

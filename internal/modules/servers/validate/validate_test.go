package validate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/validate"
)

func TestSeedHasOnlyLatestWarnings(t *testing.T) {
	r := validate.Validate(context.Background(), validate.Input{Slug: seed.Slug, Files: seed.Files()})
	for _, f := range r.Errors() {
		t.Errorf("unexpected error: %s", f)
	}
	var latest []string
	for _, f := range r.Warnings() {
		latest = append(latest, f.Message)
	}
	if len(latest) != 2 {
		t.Fatalf("want exactly two warnings, got %d: %v", len(latest), latest)
	}
	joined := strings.Join(latest, "\n")
	for _, img := range []string{"certbot/certbot:latest", "ghcr.io/xtls/xray-core:latest"} {
		if !strings.Contains(joined, img) {
			t.Errorf("no warning names %s: %s", img, joined)
		}
	}
	if strings.Contains(joined, "nginx:stable-alpine") {
		t.Error("nginx:stable-alpine is pinned and must not warn")
	}
}

func TestDroppedEndpointKeyWarns(t *testing.T) {
	prev, fs := manifest.Parse(seedWith(nil)["manifest.yaml"])
	if len(fs) != 0 {
		t.Fatal(fs)
	}
	m := string(seedWith(nil)["manifest.yaml"])
	renamed := strings.Replace(m, "key: main ", "key: primary ", 1)
	files := seedWith(map[string]string{"manifest.yaml": renamed})
	r := validate.Validate(t.Context(), validate.Input{Slug: "t", Files: files, Previous: prev})
	if !has(r, finding.Warning, "endpoints", "manifest.yaml", 0, `"main"`) {
		t.Errorf("a dropped endpoint key must warn: %v", r.Findings)
	}
	// the same manifest as before warns about nothing
	r = validate.Validate(t.Context(), validate.Input{Slug: "t", Files: seedWith(nil), Previous: prev})
	for _, f := range r.Warnings() {
		if f.Check == "endpoints" {
			t.Errorf("unexpected: %s", f)
		}
	}
}

func TestEndpointShape(t *testing.T) {
	m := string(seedWith(nil)["manifest.yaml"])
	dup := strings.Replace(m, "checks:", "  - key: main\n    type: vless-xhttp-tls\n    host: x.example.com\n    port: 443\n    credential: \"{{ .Gen.client_uuid }}\"\n\nchecks:", 1)
	r := run(t, seedWith(map[string]string{"manifest.yaml": dup}))
	if !has(r, finding.Error, "manifest", "manifest.yaml", 0, "used twice") {
		t.Errorf("duplicate endpoint keys must be an error: %v", r.Findings)
	}
	port := strings.Replace(m, "    port: 443\n", "    port: 70000\n", 1)
	r = run(t, seedWith(map[string]string{"manifest.yaml": port}))
	if !has(r, finding.Error, "endpoints", "manifest.yaml", 0, "70000") {
		t.Errorf("a port of 70000 must be an error: %v", r.Findings)
	}
}

package proxy_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/validate"
)

// wired puts the validators' hooks where the servers module does.
func wired(t *testing.T) {
	t.Helper()
	validate.XrayConfig, validate.EndpointFields = proxy.ValidateConfig, endpoint.Check
	t.Cleanup(func() { validate.XrayConfig, validate.EndpointFields = nil, nil })
}

func TestSeedXrayConfigAccepted(t *testing.T) {
	files := seed.Files()
	m, fs := manifest.Parse(files[manifest.Name])
	if m == nil {
		t.Fatalf("seed manifest: %v", fs)
	}
	rendered, rf := render.Render(m, files, render.SampleContext(m, seed.Slug, nil))
	if len(rf) != 0 {
		t.Fatalf("render findings: %v", rf)
	}
	var xray []byte
	for _, f := range rendered.Files {
		if f.Path == "xray-config.json" {
			xray = f.Content
		}
	}
	if len(xray) == 0 {
		t.Fatal("the seed has no xray-config.json")
	}
	if err := proxy.ValidateConfig(xray); err != nil {
		t.Errorf("xray refuses the seed's config: %v", err)
	}

	// and through the validators, nothing from the xray or endpoints checks
	wired(t)
	rep := validate.Validate(t.Context(), validate.Input{Slug: seed.Slug, Files: files})
	for _, f := range rep.Findings {
		if f.Check == "xray" || f.Check == "endpoints" {
			t.Errorf("unexpected finding: %s", f)
		}
	}
}

func TestXrayRejectsUnknownNetwork(t *testing.T) {
	wired(t)
	files := seed.Files()
	files["xray-config.json"] = bytes.Replace(files["xray-config.json"], []byte(`"network": "xhttp"`), []byte(`"network": "carrier-pigeon"`), 1)
	rep := validate.Validate(t.Context(), validate.Input{Slug: seed.Slug, Files: files})
	var got *finding.Finding
	for i, f := range rep.Findings {
		if f.Check == "xray" {
			got = &rep.Findings[i]
		}
	}
	if got == nil || got.Severity != finding.Error || got.Path != "xray-config.json" || !strings.Contains(strings.ToLower(got.Message), "carrier-pigeon") {
		t.Fatalf("the xray check should fail on the network with xray's own words, got %v\nall: %v", got, rep.Findings)
	}
	if rep.OK() {
		t.Error("a config xray refuses must not publish")
	}
}

func TestValidateConfigRefusals(t *testing.T) {
	for name, cfg := range map[string]string{
		"not json":   `{"inbounds": [`,
		"not object": `[]`,
		"null":       `null`,
		"bad port":   `{"inbounds":[{"port":"x","protocol":"vless","settings":{"clients":[],"decryption":"none"}}]}`,
	} {
		if err := proxy.ValidateConfig([]byte(cfg)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := proxy.ValidateConfig([]byte(`{"outbounds":[{"protocol":"freedom"}]}`)); err != nil {
		t.Errorf("a minimal config: %v", err)
	}
}

package render_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
)

func seedManifest(t *testing.T) (*manifest.Manifest, map[string][]byte) {
	t.Helper()
	files := seed.Files()
	m, fs := manifest.Parse(files["manifest.yaml"])
	if len(fs) != 0 {
		t.Fatal(fs)
	}
	return m, files
}

func TestMissingKeyIsAnError(t *testing.T) {
	m, files := seedManifest(t)
	files["xray-config.json"] = []byte(strings.Replace(string(files["xray-config.json"]), ".Gen.xhttp_path", ".Gen.client_uid", 1))
	c := render.SampleContext(m, "t", nil)
	_, fs := render.Render(m, files, c)
	if len(fs) != 1 {
		t.Fatalf("one error expected: %v", fs)
	}
	f := fs[0]
	if f.Severity != finding.Error || f.Path != "xray-config.json" || f.Line != 20 || !strings.Contains(f.Message, "client_uid") {
		t.Errorf("want the key and xray-config.json:20, got %s", f)
	}
	// a syntax error in a template has its line too
	files["site/index.html"] = []byte("<p>\n{{ if }}\n")
	_, fs = render.Render(m, files, c)
	var found bool
	for _, f := range fs {
		if f.Path == "site/index.html" && f.Line == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("a parse error must carry its line: %v", fs)
	}
}

func TestMissingKeyInAManifestFieldNamesIt(t *testing.T) {
	m, files := seedManifest(t)
	m.Steps.Install[4].Args["url"] = "https://{{ .Server.ProxyHostnam }}/"
	_, fs := render.Render(m, files, render.SampleContext(m, "t", nil))
	if len(fs) != 1 || fs[0].Path != "manifest.yaml" || !strings.Contains(fs[0].Message, "steps.install[4].url") || fs[0].Line == 0 {
		t.Errorf("want the field's name, manifest.yaml and a line: %v", fs)
	}
}

func TestClientsHoldSharedCredential(t *testing.T) {
	m, files := seedManifest(t)
	c := render.SampleContext(m, "t", nil)
	out, fs := render.Render(m, files, c)
	if len(fs) != 0 {
		t.Fatal(fs)
	}
	uuid := c.Gen["client_uuid"]
	if uuid == "" {
		t.Fatal("no generated uuid")
	}
	var xray string
	for _, f := range out.Files {
		if f.Path == "xray-config.json" {
			xray = string(f.Content)
		}
	}
	if !strings.Contains(xray, `{ "id": "`+uuid+`", "email": "shared" }`) {
		t.Errorf("clients must hold the endpoint credential as 'shared':\n%s", xray)
	}
	if !strings.Contains(xray, `"path": "`+c.Gen["xhttp_path"]+`"`) || !strings.HasPrefix(c.Gen["xhttp_path"], "/") {
		t.Errorf("the path must come from the generated value:\n%s", xray)
	}
	if len(out.Endpoints) != 1 || out.Endpoints[0].Credential != uuid || out.Endpoints[0].Port != 443 ||
		out.Endpoints[0].Host != "xx-1.hosts.example.invalid" || out.Endpoints[0].Params["path"] != c.Gen["xhttp_path"] {
		t.Errorf("endpoints: %+v", out.Endpoints)
	}
	// files keep the manifest's order and modes, and the .env is rendered
	if out.Files[0].Path != "compose.yaml" || out.Files[4].Path != ".env" || out.Files[4].Mode != 0o600 {
		t.Errorf("files: %v", out.Files)
	}
	if !strings.Contains(string(out.Files[4].Content), "SERVER_DOMAIN=xx-1.hosts.example.invalid") {
		t.Errorf(".env: %s", out.Files[4].Content)
	}
	// rendered steps and checks
	if out.Install[4].Args["url"] != "https://xx-1.hosts.example.invalid/" {
		t.Errorf("wait-http: %+v", out.Install[4])
	}
	if out.Checks[2].Args["file"] != "certbot/conf/live/xx-1.hosts.example.invalid/fullchain.pem" {
		t.Errorf("checks: %+v", out.Checks)
	}
	// a caller's own clients win (per-link credentials later)
	c.Clients = []render.Client{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	out, _ = render.Render(m, files, c)
	if !strings.Contains(string(out.Files[1].Content), `"id": "a", "email": "A" },{ "id": "b"`) {
		t.Errorf("explicit clients: %s", out.Files[1].Content)
	}
}

func TestOptionalParameterIsEmptyNotMissing(t *testing.T) {
	m, files := seedManifest(t)
	c := render.SampleContext(m, "t", nil)
	delete(c.Params, "letsencrypt_email")
	out, fs := render.Render(m, files, c)
	if len(fs) != 0 {
		t.Fatalf("a declared parameter that was not given renders empty: %v", fs)
	}
	if !strings.Contains(string(out.Files[5].Content), "EMAIL=''") {
		t.Errorf("script: %s", out.Files[5].Content)
	}
}

func TestEndpointPortMustBeAPort(t *testing.T) {
	m, files := seedManifest(t)
	m.Endpoints[0].Port = "{{ .Server.SSHPort }}0000"
	_, fs := render.Render(m, files, render.SampleContext(m, "t", nil))
	if len(fs) != 1 || fs[0].Check != "endpoints" || !strings.Contains(fs[0].Message, "220000") {
		t.Errorf("%v", fs)
	}
}

func TestHostnamePattern(t *testing.T) {
	h, err := render.Hostname("{location}-{number}.hosts.tikhonnnnn.com", "nl", 1)
	if err != nil || h != "nl-1.hosts.tikhonnnnn.com" {
		t.Errorf("%q %v", h, err)
	}
	for _, bad := range []string{
		"{location}.hosts.tikhonnnnn.com",  // no number
		"x-{number}.hosts.tikhonnnnn.com",  // no location
		"{location}_{number}.example.com",  // underscore
		"{location}-{number}",              // not fully qualified
		"{location}-{number}.-bad.example", // label starts with -
		"{location}-{number}.exa mple.com", // space
		"{location}-{number}..example.com", // empty label
	} {
		if _, err := render.Hostname(bad, "nl", 1); err == nil {
			t.Errorf("pattern %q must be refused", bad)
		}
	}
}

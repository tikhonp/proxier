package manifest_test

import (
	"reflect"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
)

func str(s string) *string { return &s }

func TestParseSeedManifest(t *testing.T) {
	m, fs := manifest.Parse(seed.Files()["manifest.yaml"])
	if len(fs) != 0 {
		t.Fatalf("findings: %v", fs)
	}
	if m.Name != "VLESS XHTTP behind nginx" || m.Dir != "/opt/proxier/vless-xhttp" || !m.HasRequires {
		t.Errorf("header: %+v", m)
	}
	if !reflect.DeepEqual(m.Requires, manifest.Requires{
		OS: []string{"debian-12", "debian-13", "ubuntu-22.04", "ubuntu-24.04"}, Arch: []string{"amd64", "arm64"}}) {
		t.Errorf("requires: %+v", m.Requires)
	}
	if len(m.Ports) != 2 || m.Ports[0].Number != 80 || m.Ports[1].Proto != "tcp" {
		t.Errorf("ports: %+v", m.Ports)
	}
	if len(m.Parameters) != 1 || m.Parameters[0].Key != "letsencrypt_email" || m.Parameters[0].Type != "email" ||
		m.Parameters[0].Required || !reflect.DeepEqual(m.Parameters[0].Sample, str("admin@example.com")) {
		t.Errorf("parameters: %+v", m.Parameters)
	}
	wantGen := []string{"client_uuid:uuid:true", "xhttp_path:hex:true", "container_postfix:hex:false"}
	for i, g := range m.Generated {
		if got := g.Key + ":" + g.Kind + ":" + map[bool]string{true: "true", false: "false"}[g.Rotate]; got != wantGen[i] {
			t.Errorf("generated[%d] = %s, want %s", i, got, wantGen[i])
		}
	}
	if m.Generated[1].Length != 16 || m.Generated[1].Prefix != "/" {
		t.Errorf("xhttp_path: %+v", m.Generated[1])
	}
	if len(m.Files) != 6 || m.Files[4].Path != ".env" || m.Files[4].Mode != 0o600 || m.Files[5].Mode != 0o755 || m.Files[0].Mode != 0o644 || m.Files[5].Validate != "shell" {
		t.Errorf("files: %+v", m.Files)
	}
	kinds := func(steps []manifest.Step) (out []string) {
		for _, s := range steps {
			out = append(out, s.Kind)
		}
		return
	}
	if got := kinds(m.Steps.Install); !reflect.DeepEqual(got, []string{"base-bootstrap", "upload-files", "run", "compose-up", "wait-http"}) {
		t.Errorf("install: %v", got)
	}
	if got := kinds(m.Steps.Redeploy); !reflect.DeepEqual(got, []string{"upload-files", "compose-up"}) {
		t.Errorf("redeploy: %v", got)
	}
	run := m.Steps.Install[2]
	if run.Args["run"] != "./issue-cert.sh" || run.Args["timeout"] != "5m" || run.Pos != "steps.install[2]" {
		t.Errorf("run step: %+v", run)
	}
	if m.Steps.Install[3].Args["pull"] != true {
		t.Errorf("compose-up: %+v", m.Steps.Install[3])
	}
	if w := m.Steps.Install[4].Args; w["status"] != 200 || w["resolve"] != "127.0.0.1" {
		t.Errorf("wait-http: %+v", w)
	}
	if m.Steps.Uninstall[0].Args["volumes"] != true {
		t.Errorf("uninstall: %+v", m.Steps.Uninstall)
	}
	if len(m.Endpoints) != 1 || m.Endpoints[0].Key != "main" || m.Endpoints[0].Port != "443" ||
		m.Endpoints[0].Params["mode"] != "stream-up" || m.Endpoints[0].Credential != "{{ .Gen.client_uuid }}" {
		t.Errorf("endpoints: %+v", m.Endpoints)
	}
	if len(m.Checks) != 4 || m.Checks[0].Kind != "compose-running" || m.Checks[2].Args["file"] == nil || m.Checks[3].Kind != "disk-free" {
		t.Errorf("checks: %+v", m.Checks)
	}
	if m.ProxyTest == nil || m.ProxyTest.URL != "https://speed.cloudflare.com/__down?bytes=262144" {
		t.Errorf("proxy_test: %+v", m.ProxyTest)
	}
	if fs := manifest.Verify(m, seed.Files()); len(fs) != 0 {
		t.Errorf("the seed must verify clean: %v", fs)
	}
}

func TestParseReportsYAMLProblemsWithLines(t *testing.T) {
	_, fs := manifest.Parse([]byte("name: x\nsteps: [\n"))
	if len(fs) != 1 || fs[0].Line == 0 {
		t.Errorf("a syntax error needs a line: %v", fs)
	}
	_, fs = manifest.Parse([]byte("name: x\nstep: {}\n"))
	if len(fs) != 1 || fs[0].Line != 2 {
		t.Errorf("an unknown field is reported on its line: %v", fs)
	}
}

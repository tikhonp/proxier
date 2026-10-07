package manifest_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
)

// verify parses the seed's manifest with one replacement and verifies it
// against the seed's tree (with the manifest swapped).
func verify(t *testing.T, old, new string) []finding.Finding {
	t.Helper()
	files := seed.Files()
	src := string(files["manifest.yaml"])
	if !strings.Contains(src, old) {
		t.Fatalf("the seed manifest has no %q", old)
	}
	files["manifest.yaml"] = []byte(strings.Replace(src, old, new, 1))
	m, fs := manifest.Parse(files["manifest.yaml"])
	if m == nil {
		t.Fatalf("parse: %v", fs)
	}
	return append(fs, manifest.Verify(m, files)...)
}

func hasErr(fs []finding.Finding, text string) *finding.Finding {
	for i, f := range fs {
		if f.Severity == finding.Error && strings.Contains(f.Message, text) {
			return &fs[i]
		}
	}
	return nil
}

func TestManifestSchemaErrors(t *testing.T) {
	cases := []struct{ name, old, new, want string }{
		{"step kind", "    - compose-up: { pull: true }", "    - compose-upp: { pull: true }", `unknown step kind "compose-upp"`},
		{"check kind", "  - disk-free", "  - disk-fre", `unknown check kind "disk-fre"`},
		{"generated kind", "kind: uuid", "kind: uuidv9", `unknown kind "uuidv9"`},
		{"validator", "validate: shell", "validate: bash", `unknown validator "bash"`},
		{"endpoint type", "type: vless-xhttp-tls", "type: vmess", `unknown type "vmess"`},
		{"duplicate generated", "  - { key: container_postfix", "  - { key: client_uuid, kind: uuid }\n  - { key: container_postfix", "declared twice"},
		{"duplicate file", "  - { path: site/index.html }", "  - { path: site/index.html }\n  - { path: site/index.html }", "declared twice"},
		{"bad port", "ports: [80/tcp, 443/tcp]", "ports: [80/tcp, 99999/tcp]", "must look like 443/tcp"},
		{"bad proto", "ports: [80/tcp, 443/tcp]", "ports: [80/icmp]", "must look like 443/tcp"},
		{"relative dir", "dir: /opt/proxier/vless-xhttp", "dir: opt/proxier", "must be an absolute path"},
		{"root dir", "dir: /opt/proxier/vless-xhttp", "dir: /", "may not be /"},
		{"hex length", "kind: hex, length: 16", "kind: hex, length: 2", "length must be 4–128"},
		{"step field", "compose-up: { pull: true }", "compose-up: { pul: true }", `has no field "pul"`},
		{"bad timeout", "timeout: 5m", "timeout: soon", "not a duration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := verify(t, c.old, c.new)
			f := hasErr(fs, c.want)
			if f == nil {
				t.Fatalf("no error containing %q in %v", c.want, fs)
			}
			if f.Line == 0 && c.name != "relative dir" && c.name != "root dir" {
				t.Errorf("the error has no line: %s", f)
			}
		})
	}
}

func TestStepRefersToMissingFile(t *testing.T) {
	files := seed.Files()
	delete(files, "issue-cert.sh")
	src := strings.Replace(string(files["manifest.yaml"]), "  - { path: issue-cert.sh, mode: \"0755\", validate: shell }\n", "", 1)
	files["manifest.yaml"] = []byte(src)
	m, _ := manifest.Parse(files["manifest.yaml"])
	f := hasErr(manifest.Verify(m, files), "./issue-cert.sh is not among the files")
	if f == nil {
		t.Fatal("a step running a file the template doesn't ship must be an error")
	}
	if !strings.Contains(f.Message, "steps.install[2]") || f.Line == 0 {
		t.Errorf("the error must name the step and its line: %s", f)
	}
}

func TestStepOrderRules(t *testing.T) {
	cases := []struct{ name, old, new, want string }{
		{"install starts with something else", "    - base-bootstrap\n    - upload-files\n", "    - upload-files\n    - base-bootstrap\n", "base-bootstrap must be the first install step"},
		{"upload-files second", "    - base-bootstrap\n    - upload-files\n", "    - base-bootstrap\n    - compose-up\n    - upload-files\n", "upload-files must be the second install step"},
		{"base-bootstrap later", "    - compose-up: { pull: true }\n", "    - compose-up: { pull: true }\n    - base-bootstrap\n", "base-bootstrap may only be at the start of install"},
		{"redeploy not starting with upload-files", "  redeploy:                          # redeploy, upgrade, rotation\n    - upload-files\n    - compose-up\n", "  redeploy:\n    - compose-up\n    - upload-files\n", "upload-files must be the first redeploy step"},
		{"upload-files in uninstall", "    - compose-down: { volumes: true }", "    - upload-files\n    - compose-down: { volumes: true }", "upload-files cannot be in uninstall"},
		{"base-bootstrap in redeploy", "    - upload-files\n    - compose-up\n  uninstall", "    - upload-files\n    - base-bootstrap\n  uninstall", "base-bootstrap may only be at the start of redeploy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := verify(t, c.old, c.new)
			if f := hasErr(fs, c.want); f == nil {
				t.Fatalf("no error containing %q in %v", c.want, fs)
			} else if f.Line == 0 {
				t.Errorf("no line: %s", f)
			}
		})
	}
	if fs := verify(t, "timeout: 5m", "timeout: 6m"); len(fs) != 0 {
		t.Errorf("a valid order must pass: %v", fs)
	}
}

func TestMissingRequiresWarns(t *testing.T) {
	fs := verify(t, "requires:\n  os: [debian-12, debian-13, ubuntu-22.04, ubuntu-24.04]\n  arch: [amd64, arm64]\n", "")
	for _, f := range fs {
		if f.Severity == finding.Warning && strings.Contains(f.Message, "requires is missing") {
			return
		}
		if f.Severity == finding.Error {
			t.Errorf("a missing requires is only a warning: %s", f)
		}
	}
	t.Errorf("no warning in %v", fs)
}

func TestRequiredParameterNeedsSample(t *testing.T) {
	fs := verify(t, "    required: false\n    sample: admin@example.com        # used by validation\n", "    required: true\n")
	f := hasErr(fs, "give a sample value")
	if f == nil || f.Check != "parameters" {
		t.Fatalf("a required parameter without sample or default must say so: %v", fs)
	}
	// a default is as good as a sample
	if fs := verify(t, "    required: false\n    sample: admin@example.com        # used by validation\n", "    required: true\n    default: a@b.example\n"); len(fs) != 0 {
		t.Errorf("a default is enough: %v", fs)
	}
}

func TestFilePathRules(t *testing.T) {
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "a/../b", "./a", "a b", "a;b", "a$b", "a//b", "a/", "", strings.Repeat("a", 256), "ü.txt"} {
		if manifest.ValidPath(bad) == nil {
			t.Errorf("path %q must be refused", bad)
		}
	}
	for _, ok := range []string{"compose.yaml", ".env", "nginx/default.conf.template", "a_b-c/d.e"} {
		if err := manifest.ValidPath(ok); err != nil {
			t.Errorf("path %q must pass: %v", ok, err)
		}
	}
	// in the manifest and in the tree
	if fs := verify(t, "  - { path: site/index.html }", "  - { path: ../site/index.html }"); hasErr(fs, "relative") == nil && hasErr(fs, "may not contain") == nil {
		t.Errorf("a .. path in files must be an error: %v", fs)
	}
	files := seed.Files()
	files["extra.txt"] = []byte("x")
	files["bad name.txt"] = []byte("x")
	m, _ := manifest.Parse(files["manifest.yaml"])
	fs := manifest.Verify(m, files)
	if hasErr(fs, "not declared in the manifest") == nil {
		t.Errorf("an undeclared file must be an error: %v", fs)
	}
	var onBad bool
	for _, f := range fs {
		if f.Path == "bad name.txt" {
			onBad = true
		}
	}
	if !onBad {
		t.Errorf("a bad name in the tree must be an error on that path: %v", fs)
	}
	// a declared file that is not there
	delete(files, "site/index.html")
	if hasErr(manifest.Verify(m, files), "declared but not in the template") == nil {
		t.Error("a declared file that's missing must be an error")
	}
}

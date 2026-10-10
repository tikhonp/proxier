// Package validate checks a template version before it can be published
// (docs/processes/servers/template-authoring.md#validation): the manifest, a
// render for the sample context, and every file by its validator. All pure Go:
// nothing starts a container or a process, and the image stays distroless
// (ADR 0009).
package validate

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

// Input is a version or a draft to check.
type Input struct {
	Slug     string
	Files    map[string][]byte  // includes manifest.yaml
	Previous *manifest.Manifest // the latest version's, for dropped endpoint keys; nil for none
}

// Hooks the module fills in Init. Nil means the check finds nothing.
var (
	// XrayConfig is proxy.ValidateConfig (1c): it refuses a config xray-core
	// cannot build an instance from.
	XrayConfig func(rendered []byte) error
	// EndpointFields is endpoint.Check (1c): the per-type checks of rendered
	// endpoint fields.
	EndpointFields func(e render.RenderedEndpoint) []string
)

// Validate runs every check. It never fails: problems are findings.
func Validate(ctx context.Context, in Input) finding.Report {
	var fs []finding.Finding
	fs = append(fs, sizeFindings(in.Files)...)

	raw, ok := in.Files[manifest.Name]
	if !ok {
		fs = append(fs, finding.Errorf("manifest", manifest.Name, 0, "%s is missing", manifest.Name))
		return report(fs)
	}
	m, parseFindings := manifest.Parse(raw)
	fs = append(fs, parseFindings...)
	if m == nil || hasError(parseFindings) {
		// A manifest that cannot be read gives nothing sensible to render.
		return report(fs)
	}
	fs = append(fs, manifest.Verify(m, in.Files)...)

	c := render.SampleContext(m, in.Slug, nil)
	c.Template.Version = 0
	rendered, rf := render.Render(m, in.Files, c)
	fs = append(fs, rf...)

	files := map[string]render.RenderedFile{}
	for _, f := range rendered.Files {
		files[f.Path] = f
	}
	cp := &composeCache{}
	for _, mf := range m.Files {
		if ctx.Err() != nil {
			break
		}
		f, ok := files[mf.Path]
		if !ok {
			continue // not rendered: its findings are above
		}
		fs = append(fs, fileFindings(mf, f, files, cp)...)
	}

	if EndpointFields != nil {
		for _, e := range rendered.Endpoints {
			for _, msg := range EndpointFields(e) {
				fs = append(fs, finding.Errorf("endpoints", manifest.Name, endpointLine(m, e.Key), "endpoint %q: %s", e.Key, msg))
			}
		}
	}
	fs = append(fs, droppedEndpoints(m, in.Previous)...)
	return report(fs)
}

func fileFindings(mf manifest.File, f render.RenderedFile, files map[string]render.RenderedFile, cp *composeCache) []finding.Finding {
	switch mf.Validate {
	case "json":
		return jsonFindings("json", f)
	case "yaml":
		return yamlFindings(f)
	case "shell":
		return shellFindings(f)
	case "compose":
		return cp.findings(f, files)
	case "nginx":
		return nginxFindings(f, files, cp)
	case "xray":
		if XrayConfig == nil {
			return nil
		}
		if err := XrayConfig(f.Content); err != nil {
			return []finding.Finding{finding.Errorf("xray", f.Path, 0, "%v", err)}
		}
	}
	return nil
}

func endpointLine(m *manifest.Manifest, key string) int {
	for _, e := range m.Endpoints {
		if e.Key == key {
			return e.Line
		}
	}
	return 0
}

// droppedEndpoints warns for every endpoint key the previous version had and
// this one lacks: servers upgrading lose it from their subscriptions.
func droppedEndpoints(m, prev *manifest.Manifest) []finding.Finding {
	if prev == nil {
		return nil
	}
	have := map[string]bool{}
	for _, e := range m.Endpoints {
		have[e.Key] = true
	}
	var out []finding.Finding
	for _, e := range prev.Endpoints {
		if !have[e.Key] {
			out = append(out, finding.Warnf("endpoints", manifest.Name, 0,
				"endpoint %q of the previous version is gone: servers upgrading will lose it from their subscriptions", e.Key))
		}
	}
	return out
}

func hasError(fs []finding.Finding) bool {
	return slices.ContainsFunc(fs, func(f finding.Finding) bool { return f.Severity == finding.Error })
}

// report orders findings by file, line and severity, so a repeated validation
// of the same input reads the same.
func report(fs []finding.Finding) finding.Report {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Severity < b.Severity
	})
	return finding.Report{Findings: fs}
}

// lineOf is the 1-based line of the first line containing needle, or 0.
func lineOf(content []byte, needle string) int {
	line := 1
	start := 0
	for i := 0; i <= len(content); i++ {
		if i == len(content) || content[i] == '\n' {
			if needle != "" && containsBytes(content[start:i], needle) {
				return line
			}
			line++
			start = i + 1
		}
	}
	return 0
}

func containsBytes(b []byte, s string) bool {
	return len(s) > 0 && len(b) >= len(s) && indexOf(b, s) >= 0
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}

func lineAt(content []byte, offset int64) int {
	line := 1
	for i := int64(0); i < offset && i < int64(len(content)); i++ {
		if content[i] == '\n' {
			line++
		}
	}
	return line
}

func errorf(check string, f render.RenderedFile, line int, format string, args ...any) finding.Finding {
	return finding.Errorf(check, f.Path, line, "%s", fmt.Sprintf(format, args...))
}

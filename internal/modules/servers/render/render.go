package render

import (
	"bytes"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

// Rendered is a version rendered for one server.
type Rendered struct {
	Files                        []RenderedFile  // in manifest order
	Install, Redeploy, Uninstall []manifest.Step // Args with strings rendered
	Endpoints                    []RenderedEndpoint
	Checks                       []manifest.Check
	ProxyTestURL                 string // "" = the setting
}

type RenderedFile struct {
	Path    string
	Mode    fs.FileMode
	Content []byte
}

type RenderedEndpoint struct {
	Key, Type, Host, Credential string
	Port                        int
	Params                      map[string]string
}

// Render renders a version for c. files is the tree (path → raw content).
// It reports every problem it can find, not just the first. A file or field
// that fails is left out, so its findings explain the gap.
func Render(m *manifest.Manifest, files map[string][]byte, c Context) (*Rendered, []finding.Finding) {
	r := &renderer{c: c}
	// Parameters not given take their default, or are empty, so an optional
	// one can be tested with `if`.
	r.c.Params = map[string]any{}
	for k, v := range c.Params {
		r.c.Params[k] = v
	}
	for _, p := range m.Parameters {
		if _, ok := r.c.Params[p.Key]; !ok {
			raw := ""
			if p.Default != nil {
				raw = *p.Default
			}
			r.c.Params[p.Key] = ParamValue(p, raw)
		}
	}
	if r.c.Gen == nil {
		r.c.Gen = map[string]string{}
	}

	out := &Rendered{}

	// Pass one: endpoints. A credential may use .Gen and .Params only, and
	// becomes the shared client the files then see in .Clients.
	r.c.Clients = nil
	for _, e := range m.Endpoints {
		re, ok := r.endpoint(e)
		if ok {
			out.Endpoints = append(out.Endpoints, re)
		}
	}
	if c.Clients != nil {
		r.c.Clients = c.Clients
	} else {
		for _, e := range out.Endpoints {
			if e.Credential != "" {
				r.c.Clients = []Client{{ID: e.Credential, Name: SharedClient}}
				break
			}
		}
	}

	for _, f := range m.Files {
		raw, ok := files[f.Path]
		if !ok {
			continue // manifest.Verify reports a missing file
		}
		if b, ok := r.text(f.Path, f.Line, string(raw)); ok {
			out.Files = append(out.Files, RenderedFile{Path: f.Path, Mode: f.Mode, Content: []byte(b)})
		}
	}
	out.Install = r.steps(m.Steps.Install)
	out.Redeploy = r.steps(m.Steps.Redeploy)
	out.Uninstall = r.steps(m.Steps.Uninstall)
	for _, ch := range m.Checks {
		args, ok := r.args(ch.Pos, ch.Line, ch.Args)
		if ok {
			out.Checks = append(out.Checks, manifest.Check{Kind: ch.Kind, Args: args, Pos: ch.Pos, Line: ch.Line})
		}
	}
	if m.ProxyTest != nil {
		if s, ok := r.text("proxy_test.url", m.ProxyTest.Line, m.ProxyTest.URL); ok {
			out.ProxyTestURL = s
		}
	}
	return out, r.fs
}

type renderer struct {
	c  Context
	fs []finding.Finding
}

// errLine matches text/template's "template: name:line:col: …" and
// "template: name:line: …".
var errLine = regexp.MustCompile(`^template: (.+?):(\d+)(?::\d+)?: (.*)$`)

// text renders one template named name. For files the name is the path; for
// manifest fields it is "manifest.yaml#<where>", and base is the line of the
// field in the manifest.
func (r *renderer) text(name string, base int, src string) (string, bool) {
	t, err := template.New(name).Funcs(Funcs()).Option("missingkey=error").Parse(src)
	if err == nil {
		var buf bytes.Buffer
		if err = t.Execute(&buf, r.c); err == nil {
			return buf.String(), true
		}
	}
	r.fail(name, base, err)
	return "", false
}

func (r *renderer) fail(name string, base int, err error) {
	path, line, msg := name, 0, err.Error()
	if m := errLine.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[2])
		msg = m[3]
	}
	if i := strings.Index(name, "#"); i >= 0 {
		// A manifest field: the line is the field's, a template error line is
		// inside the field.
		path = name[:i]
		if base > 0 {
			line = base
		}
		msg = name[i+1:] + ": " + msg
	} else if line == 0 {
		line = base
	}
	r.fs = append(r.fs, finding.Errorf("render", path, line, "%s", msg))
}

func (r *renderer) field(where string, line int, src string) (string, bool) {
	if !strings.Contains(src, "{{") {
		return src, true
	}
	return r.text(manifest.Name+"#"+where, line, src)
}

func (r *renderer) steps(in []manifest.Step) []manifest.Step {
	var out []manifest.Step
	for _, s := range in {
		args, ok := r.args(s.Pos, s.Line, s.Args)
		if ok {
			out = append(out, manifest.Step{Kind: s.Kind, Args: args, Pos: s.Pos, Line: s.Line})
		}
	}
	return out
}

// args renders the string values; numbers and booleans stay as they are.
func (r *renderer) args(pos string, line int, in map[string]any) (map[string]any, bool) {
	out := make(map[string]any, len(in))
	ok := true
	for k, v := range in {
		s, isStr := v.(string)
		if !isStr {
			out[k] = v
			continue
		}
		rs, good := r.field(pos+"."+k, line, s)
		out[k] = rs
		ok = ok && good
	}
	return out, ok
}

func (r *renderer) endpoint(e manifest.Endpoint) (RenderedEndpoint, bool) {
	where := "endpoints." + e.Key
	re := RenderedEndpoint{Key: e.Key, Type: e.Type, Params: map[string]string{}}
	ok := true
	str := func(field, src string) string {
		s, good := r.field(where+"."+field, e.Line, src)
		ok = ok && good
		return s
	}
	re.Host = str("host", e.Host)
	re.Credential = str("credential", e.Credential)
	port := str("port", e.Port)
	for k, v := range e.Params {
		re.Params[k] = str("params."+k, v)
	}
	if ok {
		n, err := strconv.Atoi(strings.TrimSpace(port))
		if err != nil || n < 1 || n > 65535 {
			r.fs = append(r.fs, finding.Errorf("endpoints", manifest.Name, e.Line, "endpoint %q: the port renders to %q, which is not a port number (1–65535)", e.Key, port))
			return re, false
		}
		re.Port = n
	}
	return re, ok
}

// Describe names a rendered file for messages.
func (f RenderedFile) Describe() string { return fmt.Sprintf("%s (%04o)", f.Path, f.Mode) }

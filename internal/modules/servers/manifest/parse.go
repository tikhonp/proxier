package manifest

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"go.yaml.in/yaml/v3"
)

type parser struct {
	fs []finding.Finding
}

func (p *parser) errAt(n *yaml.Node, format string, args ...any) {
	line := 0
	if n != nil {
		line = n.Line
	}
	p.fs = append(p.fs, finding.Errorf("manifest", Name, line, format, args...))
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

// Parse reads manifest.yaml. It reports what cannot be read as the schema's
// shapes (wrong types, unknown fields, YAML syntax) with lines; whether the
// values make sense is Verify's job. The manifest is returned even with
// findings, as far as it could be read.
func Parse(b []byte) (*Manifest, []finding.Finding) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		line := 0
		if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		msg := strings.TrimPrefix(err.Error(), "yaml: ")
		return nil, []finding.Finding{finding.Errorf("manifest", Name, line, "%s", msg)}
	}
	p := &parser{}
	m := &Manifest{}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return m, []finding.Finding{finding.Errorf("manifest", Name, 0, "the manifest is empty")}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		p.errAt(root, "the manifest must be a mapping")
		return m, p.fs
	}
	p.top(m, root)
	return m, p.fs
}

// pairs walks a mapping's key/value nodes. It reports an unknown key.
func (p *parser) pairs(n *yaml.Node, what string, allowed []string, fn func(key string, k, v *yaml.Node)) {
	if n.Kind != yaml.MappingNode {
		p.errAt(n, "%s must be a mapping", what)
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			p.errAt(k, "%s: %q is given twice", what, k.Value)
			continue
		}
		seen[k.Value] = true
		if allowed != nil && !contains(allowed, k.Value) {
			p.errAt(k, "%s: unknown field %q", what, k.Value)
			continue
		}
		fn(k.Value, k, v)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (p *parser) scalar(n *yaml.Node, what string) (string, bool) {
	if n.Kind != yaml.ScalarNode {
		p.errAt(n, "%s must be a single value", what)
		return "", false
	}
	return n.Value, true
}

func (p *parser) boolean(n *yaml.Node, what string) bool {
	s, ok := p.scalar(n, what)
	if !ok {
		return false
	}
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	p.errAt(n, "%s must be true or false", what)
	return false
}

func (p *parser) integer(n *yaml.Node, what string) int {
	s, ok := p.scalar(n, what)
	if !ok {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		p.errAt(n, "%s must be a whole number", what)
	}
	return v
}

func (p *parser) strings(n *yaml.Node, what string) []string {
	if n.Kind != yaml.SequenceNode {
		p.errAt(n, "%s must be a list", what)
		return nil
	}
	var out []string
	for _, c := range n.Content {
		if s, ok := p.scalar(c, what); ok {
			out = append(out, s)
		}
	}
	return out
}

func (p *parser) top(m *Manifest, root *yaml.Node) {
	p.pairs(root, "the manifest", []string{"name", "description", "requires", "dir", "ports", "parameters", "generated", "files", "steps", "endpoints", "checks", "proxy_test"},
		func(key string, _, v *yaml.Node) {
			switch key {
			case "name":
				m.Name, _ = p.scalar(v, "name")
			case "description":
				m.Description, _ = p.scalar(v, "description")
			case "dir":
				m.Dir, _ = p.scalar(v, "dir")
			case "requires":
				m.HasRequires = true
				p.pairs(v, "requires", []string{"os", "arch"}, func(k string, _, vv *yaml.Node) {
					if k == "os" {
						m.Requires.OS = p.strings(vv, "requires.os")
					} else {
						m.Requires.Arch = p.strings(vv, "requires.arch")
					}
				})
			case "ports":
				p.ports(m, v)
			case "parameters":
				p.parameters(m, v)
			case "generated":
				p.generated(m, v)
			case "files":
				p.files(m, v)
			case "steps":
				p.steps(m, v)
			case "endpoints":
				p.endpoints(m, v)
			case "checks":
				m.Checks = p.checks(v)
			case "proxy_test":
				p.pairs(v, "proxy_test", []string{"url"}, func(_ string, _, vv *yaml.Node) {
					u, _ := p.scalar(vv, "proxy_test.url")
					m.ProxyTest = &ProxyTest{URL: u, Line: vv.Line}
				})
			}
		})
}

func (p *parser) ports(m *Manifest, v *yaml.Node) {
	if v.Kind != yaml.SequenceNode {
		p.errAt(v, "ports must be a list")
		return
	}
	for _, c := range v.Content {
		s, ok := p.scalar(c, "a port")
		if !ok {
			continue
		}
		num, proto, found := strings.Cut(s, "/")
		n, err := strconv.Atoi(num)
		if !found || err != nil || n < 1 || n > 65535 || (proto != "tcp" && proto != "udp") {
			p.errAt(c, "port %q must look like 443/tcp (1–65535, tcp or udp)", s)
			continue
		}
		m.Ports = append(m.Ports, Port{n, proto, c.Line})
	}
}

func (p *parser) parameters(m *Manifest, v *yaml.Node) {
	if v.Kind != yaml.SequenceNode {
		p.errAt(v, "parameters must be a list")
		return
	}
	for _, c := range v.Content {
		par := Parameter{Line: c.Line}
		p.pairs(c, "a parameter", []string{"key", "label", "type", "help", "required", "secret", "default", "sample", "options"},
			func(k string, _, vv *yaml.Node) {
				switch k {
				case "key":
					par.Key, _ = p.scalar(vv, "parameter key")
				case "label":
					par.Label, _ = p.scalar(vv, "label")
				case "type":
					par.Type, _ = p.scalar(vv, "type")
				case "help":
					par.Help, _ = p.scalar(vv, "help")
				case "required":
					par.Required = p.boolean(vv, "required")
				case "secret":
					par.Secret = p.boolean(vv, "secret")
				case "default":
					if s, ok := p.scalar(vv, "default"); ok {
						par.Default = &s
					}
				case "sample":
					if s, ok := p.scalar(vv, "sample"); ok {
						par.Sample = &s
					}
				case "options":
					par.Options = p.strings(vv, "options")
				}
			})
		m.Parameters = append(m.Parameters, par)
	}
}

func (p *parser) generated(m *Manifest, v *yaml.Node) {
	if v.Kind != yaml.SequenceNode {
		p.errAt(v, "generated must be a list")
		return
	}
	for _, c := range v.Content {
		g := Generated{Line: c.Line}
		p.pairs(c, "a generated value", []string{"key", "kind", "prefix", "length", "bytes", "rotate"},
			func(k string, _, vv *yaml.Node) {
				switch k {
				case "key":
					g.Key, _ = p.scalar(vv, "generated key")
				case "kind":
					g.Kind, _ = p.scalar(vv, "kind")
				case "prefix":
					g.Prefix, _ = p.scalar(vv, "prefix")
				case "length":
					g.Length = p.integer(vv, "length")
				case "bytes":
					g.Bytes = p.integer(vv, "bytes")
				case "rotate":
					g.Rotate = p.boolean(vv, "rotate")
				}
			})
		m.Generated = append(m.Generated, g)
	}
}

func (p *parser) files(m *Manifest, v *yaml.Node) {
	if v.Kind != yaml.SequenceNode {
		p.errAt(v, "files must be a list")
		return
	}
	for _, c := range v.Content {
		f := File{Mode: 0o644, Line: c.Line}
		p.pairs(c, "a file", []string{"path", "mode", "validate"}, func(k string, _, vv *yaml.Node) {
			switch k {
			case "path":
				f.Path, _ = p.scalar(vv, "path")
			case "validate":
				f.Validate, _ = p.scalar(vv, "validate")
			case "mode":
				s, ok := p.scalar(vv, "mode")
				if !ok {
					return
				}
				// Octal as written, quoted or not ("0755").
				n, err := strconv.ParseUint(s, 8, 32)
				if err != nil || n > 0o777 {
					p.errAt(vv, "mode %q must be an octal file mode such as \"0644\"", s)
					return
				}
				f.Mode = fs.FileMode(n)
			}
		})
		m.Files = append(m.Files, f)
	}
}

func (p *parser) steps(m *Manifest, v *yaml.Node) {
	p.pairs(v, "steps", []string{"install", "redeploy", "uninstall"}, func(k string, _, vv *yaml.Node) {
		list := p.stepList(vv, "steps."+k, stepArgs)
		switch k {
		case "install":
			m.Steps.Install = list
		case "redeploy":
			m.Steps.Redeploy = list
		case "uninstall":
			m.Steps.Uninstall = list
		}
	})
}

func (p *parser) checks(v *yaml.Node) []Check {
	steps := p.stepList(v, "checks", checkArgs)
	out := make([]Check, len(steps))
	for i, s := range steps {
		out[i] = Check(s)
	}
	return out
}

// stepList reads a list whose entries are a kind alone ("upload-files") or a
// mapping whose first key is the kind: `compose-up: {pull: true}`, or for run
// `run: ./x.sh` with its own fields beside it. Unknown kinds are kept for
// Check to name.
func (p *parser) stepList(v *yaml.Node, what string, argsOf map[string][]string) []Step {
	if v.Kind != yaml.SequenceNode {
		p.errAt(v, "%s must be a list", what)
		return nil
	}
	var out []Step
	for i, c := range v.Content {
		pos := fmt.Sprintf("%s[%d]", what, i)
		s := Step{Pos: pos, Line: c.Line, Args: map[string]any{}}
		switch c.Kind {
		case yaml.ScalarNode:
			s.Kind = c.Value
		case yaml.MappingNode:
			if len(c.Content) == 0 {
				p.errAt(c, "%s is empty", pos)
				continue
			}
			s.Kind = c.Content[0].Value
			if s.Kind == "run" {
				// run: <command> with timeout beside it.
				p.pairs(c, pos, nil, func(k string, _, vv *yaml.Node) {
					if sv, ok := p.scalar(vv, pos+" "+k); ok {
						s.Args[k] = scalarValue(vv, sv)
					}
				})
				break
			}
			val := c.Content[1]
			if len(c.Content) > 2 {
				p.errAt(c.Content[2], "%s: %q is a step of its own; put its fields in a mapping under %q", pos, c.Content[2].Value, s.Kind)
			}
			switch val.Kind {
			case yaml.MappingNode:
				p.pairs(val, pos+" "+s.Kind, nil, func(k string, _, vv *yaml.Node) {
					if sv, ok := p.scalar(vv, pos+" "+k); ok {
						s.Args[k] = scalarValue(vv, sv)
					}
				})
			case yaml.ScalarNode:
				if val.Tag != "!!null" {
					p.errAt(val, "%s: %q takes fields in a mapping", pos, s.Kind)
				}
			default:
				p.errAt(val, "%s: %q takes fields in a mapping", pos, s.Kind)
			}
		default:
			p.errAt(c, "%s must be a step name or a mapping", pos)
			continue
		}
		out = append(out, s)
	}
	return out
}

// scalarValue keeps bools and ints typed, everything else a string.
func scalarValue(n *yaml.Node, s string) any {
	switch n.Tag {
	case "!!bool":
		return s == "true"
	case "!!int":
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return s
}

func (p *parser) endpoints(m *Manifest, v *yaml.Node) {
	if v.Kind != yaml.SequenceNode {
		p.errAt(v, "endpoints must be a list")
		return
	}
	for _, c := range v.Content {
		e := Endpoint{Line: c.Line, Params: map[string]string{}}
		p.pairs(c, "an endpoint", []string{"key", "type", "host", "port", "credential", "params"}, func(k string, _, vv *yaml.Node) {
			switch k {
			case "key":
				e.Key, _ = p.scalar(vv, "endpoint key")
			case "type":
				e.Type, _ = p.scalar(vv, "endpoint type")
			case "host":
				e.Host, _ = p.scalar(vv, "host")
			case "port":
				e.Port, _ = p.scalar(vv, "port")
			case "credential":
				e.Credential, _ = p.scalar(vv, "credential")
			case "params":
				p.pairs(vv, "endpoint params", nil, func(pk string, _, pv *yaml.Node) {
					if s, ok := p.scalar(pv, "param "+pk); ok {
						e.Params[pk] = s
					}
				})
			}
		})
		m.Endpoints = append(m.Endpoints, e)
	}
}

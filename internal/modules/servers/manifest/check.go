package manifest

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/finding"
)

var (
	keyPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	endpointKeyExpr = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	pathChars       = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// ValidPath checks a file path of a template: relative, `/`-separated, plain
// characters. Paths end up in shell commands and SFTP paths, so nothing else
// is allowed.
func ValidPath(p string) error {
	switch {
	case p == "":
		return errors.New("the path is empty")
	case len(p) > 255:
		return errors.New("the path is longer than 255 bytes")
	case strings.HasPrefix(p, "/"):
		return errors.New("the path must be relative (no leading /)")
	case !pathChars.MatchString(p):
		return errors.New("the path may contain only letters, digits and . _ - /")
	case strings.HasSuffix(p, "/") || strings.Contains(p, "//"):
		return errors.New("the path has an empty part")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return fmt.Errorf("the path may not contain %q", part)
		}
	}
	return nil
}

// Verify is the schema check of a parsed manifest. files is the version's tree
// (path → content, manifest.yaml included), or nil to skip the checks that
// compare the manifest with it.
func Verify(m *Manifest, files map[string][]byte) []finding.Finding {
	c := &checker{m: m}
	c.header()
	c.parameters()
	c.generated()
	c.fileList(files)
	c.steps()
	c.endpoints()
	c.checks()
	if m.ProxyTest != nil && strings.TrimSpace(m.ProxyTest.URL) == "" {
		c.errf(m.ProxyTest.Line, "proxy_test.url is empty")
	}
	return c.fs
}

type checker struct {
	m  *Manifest
	fs []finding.Finding
}

func (c *checker) errf(line int, format string, args ...any) {
	c.fs = append(c.fs, finding.Errorf("manifest", Name, line, format, args...))
}

func (c *checker) header() {
	m := c.m
	if strings.TrimSpace(m.Name) == "" {
		c.errf(0, "name is required")
	}
	switch {
	case m.Dir == "":
		c.errf(0, "dir is required: the directory the stack lives in on the server")
	case !strings.HasPrefix(m.Dir, "/"):
		c.errf(0, "dir %q must be an absolute path", m.Dir)
	case m.Dir == "/" || strings.Trim(m.Dir, "/") == "":
		c.errf(0, "dir may not be /")
	case !pathChars.MatchString(m.Dir) || slices.Contains(strings.Split(m.Dir, "/"), ".."):
		c.errf(0, "dir %q may contain only letters, digits and . _ - / and no ..", m.Dir)
	}
	if !m.HasRequires {
		c.fs = append(c.fs, finding.Warnf("manifest", Name, 0, "requires is missing: say which operating systems and architectures the template supports"))
	}
}

func (c *checker) parameters() {
	seen := map[string]bool{}
	for _, p := range c.m.Parameters {
		switch {
		case !keyPattern.MatchString(p.Key):
			c.errf(p.Line, "parameter key %q must be lower case letters, digits and _ (starting with a letter)", p.Key)
		case seen[p.Key]:
			c.errf(p.Line, "parameter %q is declared twice", p.Key)
		}
		seen[p.Key] = true
		if p.Type == "" {
			c.errf(p.Line, "parameter %q has no type", p.Key)
		} else if !slices.Contains(ParameterTypes, p.Type) {
			c.errf(p.Line, "parameter %q: unknown type %q (one of %s)", p.Key, p.Type, strings.Join(ParameterTypes, ", "))
		}
		if p.Type == "choice" && len(p.Options) == 0 {
			c.errf(p.Line, "parameter %q is a choice without options", p.Key)
		}
		if p.Required && p.Sample == nil && p.Default == nil {
			c.fs = append(c.fs, finding.Errorf("parameters", Name, p.Line, "required parameter %q has no sample or default value: give a sample value, validation renders the template with it", p.Key))
		}
	}
}

func (c *checker) generated() {
	seen := map[string]bool{}
	for _, g := range c.m.Generated {
		switch {
		case !keyPattern.MatchString(g.Key):
			c.errf(g.Line, "generated key %q must be lower case letters, digits and _ (starting with a letter)", g.Key)
		case seen[g.Key]:
			c.errf(g.Line, "generated value %q is declared twice", g.Key)
		}
		seen[g.Key] = true
		bound := func(name string, n, lo, hi int) {
			if n < lo || n > hi {
				c.errf(g.Line, "generated %q: %s must be %d–%d", g.Key, name, lo, hi)
			}
		}
		switch g.Kind {
		case "uuid":
		case "hex":
			bound("length", g.Length, 4, 128)
		case "password":
			bound("length", g.Length, 8, 128)
		case "base64":
			bound("bytes", g.Bytes, 8, 96)
		default:
			c.errf(g.Line, "generated %q: unknown kind %q (one of %s)", g.Key, g.Kind, strings.Join(GeneratedKinds, ", "))
		}
	}
}

// fileList checks the declared files, and the tree against them.
func (c *checker) fileList(tree map[string][]byte) {
	declared := map[string]bool{Name: true}
	for _, f := range c.m.Files {
		if err := ValidPath(f.Path); err != nil {
			c.errf(f.Line, "file %q: %v", f.Path, err)
			continue
		}
		if f.Path == Name {
			c.errf(f.Line, "%s is the manifest and cannot be listed as a file", Name)
			continue
		}
		if declared[f.Path] {
			c.errf(f.Line, "file %q is declared twice", f.Path)
		}
		declared[f.Path] = true
		if f.Validate != "" && !slices.Contains(Validators, f.Validate) {
			c.errf(f.Line, "file %q: unknown validator %q (one of %s)", f.Path, f.Validate, strings.Join(Validators, ", "))
		}
		if tree != nil {
			if _, ok := tree[f.Path]; !ok {
				c.errf(f.Line, "file %q is declared but not in the template", f.Path)
			}
		}
	}
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := ValidPath(p); err != nil {
			c.fs = append(c.fs, finding.Errorf("manifest", p, 0, "%v", err))
			continue
		}
		if !declared[p] {
			c.fs = append(c.fs, finding.Errorf("manifest", p, 0, "not declared in the manifest: add it to files or delete it"))
		}
	}
}

func (c *checker) declared(path string) bool {
	for _, f := range c.m.Files {
		if f.Path == path {
			return true
		}
	}
	return false
}

func (c *checker) steps() {
	s := c.m.Steps
	if len(s.Install) == 0 {
		c.errf(0, "steps.install is required")
	}
	if len(s.Redeploy) == 0 {
		c.errf(0, "steps.redeploy is required")
	}
	for _, st := range s.Install {
		c.step("step", st, stepArgs, requiredStepArgs, StepKinds)
	}
	for _, st := range s.Redeploy {
		c.step("step", st, stepArgs, requiredStepArgs, StepKinds)
	}
	for _, st := range s.Uninstall {
		c.step("step", st, stepArgs, requiredStepArgs, StepKinds)
	}
	// The jobs run base-bootstrap and upload-files as steps of their own, with
	// DNS and generated values between them during provisioning, so their
	// place is fixed.
	c.order("install", s.Install, "base-bootstrap", "upload-files")
	c.order("redeploy", s.Redeploy, "upload-files")
	for _, st := range s.Uninstall {
		if st.Kind == "base-bootstrap" || st.Kind == "upload-files" {
			c.errf(st.Line, "%s: %s cannot be in uninstall", st.Pos, st.Kind)
		}
	}
}

func (c *checker) order(list string, steps []Step, first ...string) {
	for i, st := range steps {
		fixed := slices.Contains([]string{"base-bootstrap", "upload-files"}, st.Kind)
		switch {
		case i < len(first) && st.Kind != first[i]:
			c.errf(st.Line, "%s: %s must be the %s %s step", st.Pos, first[i], ordinal(i), list)
		case i >= len(first) && fixed:
			c.errf(st.Line, "%s: %s may only be at the start of %s", st.Pos, st.Kind, list)
		}
	}
	if len(steps) < len(first) && len(steps) > 0 {
		c.errf(steps[len(steps)-1].Line, "steps.%s must start with %s", list, strings.Join(first, ", "))
	}
}

func ordinal(i int) string {
	return [...]string{"first", "second"}[i]
}

// step checks one step or check against its kind's fields.
func (c *checker) step(what string, st Step, args map[string][]string, required map[string][]string, kinds []string) {
	if !slices.Contains(kinds, st.Kind) {
		c.errf(st.Line, "%s: unknown %s kind %q (one of %s)", st.Pos, what, st.Kind, strings.Join(kinds, ", "))
		return
	}
	for k := range st.Args {
		if !slices.Contains(args[st.Kind], k) {
			c.errf(st.Line, "%s: %s has no field %q", st.Pos, st.Kind, k)
		}
	}
	for _, k := range required[st.Kind] {
		if s, ok := st.Args[k].(string); !ok || strings.TrimSpace(s) == "" {
			c.errf(st.Line, "%s: %s needs %s", st.Pos, st.Kind, k)
		}
	}
	if t, ok := st.Args["timeout"].(string); ok && !strings.Contains(t, "{{") {
		if d, err := time.ParseDuration(t); err != nil || d <= 0 {
			c.errf(st.Line, "%s: timeout %q is not a duration such as 5m", st.Pos, t)
		}
	}
	if v, ok := st.Args["status"]; ok {
		if n, isInt := v.(int); (!isInt || n < 100 || n > 599) && !strings.Contains(fmt.Sprint(v), "{{") {
			c.errf(st.Line, "%s: status %v is not an HTTP status", st.Pos, v)
		}
	}
	for _, k := range []string{"pull", "volumes"} {
		if v, ok := st.Args[k]; ok {
			if _, isBool := v.(bool); !isBool {
				c.errf(st.Line, "%s: %s must be true or false", st.Pos, k)
			}
		}
	}
	// A script the step runs must ship with the template.
	if cmd, ok := st.Args["run"].(string); ok {
		if first := strings.Fields(cmd); len(first) > 0 && strings.HasPrefix(first[0], "./") {
			if !c.declared(strings.TrimPrefix(first[0], "./")) {
				c.errf(st.Line, "%s: %s is not among the files", st.Pos, first[0])
			}
		}
	}
}

func (c *checker) endpoints() {
	seen := map[string]bool{}
	for _, e := range c.m.Endpoints {
		switch {
		case !endpointKeyExpr.MatchString(e.Key):
			c.errf(e.Line, "endpoint key %q must be lower case letters, digits, _ and - (starting with a letter)", e.Key)
		case seen[e.Key]:
			c.errf(e.Line, "endpoint key %q is used twice", e.Key)
		}
		seen[e.Key] = true
		if !slices.Contains(EndpointTypes, e.Type) {
			c.errf(e.Line, "endpoint %q: unknown type %q (one of %s)", e.Key, e.Type, strings.Join(EndpointTypes, ", "))
		}
		if strings.TrimSpace(e.Host) == "" {
			c.errf(e.Line, "endpoint %q needs a host", e.Key)
		}
		if strings.TrimSpace(e.Port) == "" {
			c.errf(e.Line, "endpoint %q needs a port", e.Key)
		}
	}
}

func (c *checker) checks() {
	for _, ch := range c.m.Checks {
		c.step("check", Step(ch), checkArgs, requiredCheckArgs, CheckKinds)
	}
}

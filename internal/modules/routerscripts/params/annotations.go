package params

import (
	"regexp"
	"slices"
	"strings"
)

// annotation is one "# @name arg" line.
type annotation struct {
	line      int
	name, arg string
}

// readAnnotation reads an annotation line; ok is false for any other line.
func readAnnotation(text string, n int) (annotation, bool) {
	if !isAnnotation(text) {
		return annotation{}, false
	}
	t := strings.TrimSpace(commentText(text))[1:]
	name, arg, _ := strings.Cut(t, " ")
	if i := strings.IndexByte(name, '\t'); i >= 0 {
		name, arg = name[:i], name[i+1:]+" "+arg
	}
	return annotation{line: n, name: name, arg: strings.TrimSpace(arg)}, true
}

// check is the annotation's own error: an unknown name or a malformed one.
func (a annotation) check() *Finding {
	bad := func(key string, args map[string]any) *Finding {
		if args == nil {
			args = map[string]any{}
		}
		args["annotation"] = "@" + a.name
		return &Finding{Line: a.line, Severity: Error, Key: key, Args: args}
	}
	switch a.name {
	case "secret", "required":
		if a.arg != "" {
			return bad("params.err.annotation_args", nil)
		}
	case "choices":
		if _, ok := choices(a.arg); !ok {
			return bad("params.err.choices", nil)
		}
	case "pattern":
		if a.arg == "" {
			return bad("params.err.pattern_bad", map[string]any{"error": "empty"})
		}
		if _, err := regexp.Compile(anchored(a.arg)); err != nil {
			return bad("params.err.pattern_bad", map[string]any{"error": err.Error()})
		}
	case "fill":
		if !slices.Contains(FillKinds, a.arg) {
			return bad("params.err.fill_kind", map[string]any{"kinds": strings.Join(FillKinds, ", ")})
		}
	default:
		return bad("params.err.annotation_unknown", nil)
	}
	return nil
}

// choices splits "a|b|c": at least one, none empty, no duplicates.
func choices(arg string) ([]string, bool) {
	if strings.TrimSpace(arg) == "" {
		return nil, false
	}
	var out []string
	for _, c := range strings.Split(arg, "|") {
		c = strings.TrimSpace(c)
		if c == "" || slices.Contains(out, c) {
			return nil, false
		}
		out = append(out, c)
	}
	return out, true
}

// anchored makes a @pattern match the whole value.
func anchored(p string) string { return "^(?:" + p + ")$" }

// annotationsOf checks the annotations of a run and applies the good ones to
// p. Without a parameter there is nothing to apply them to: above a computed
// value that is a warning; above a line that couldn't be read the line's own
// error says enough.
func (w *walker) annotationsOf(run []int, p *Param, computed bool) {
	seen := map[string]bool{}
	fillLine := 0
	for _, i := range run {
		a, ok := readAnnotation(w.lines[i].text, i+1)
		if !ok {
			continue
		}
		if f := a.check(); f != nil {
			w.add(*f)
			continue
		}
		if p == nil {
			if computed {
				w.add(Finding{Line: a.line, Severity: Warning, Key: "params.warn.annotation_computed", Args: map[string]any{"annotation": "@" + a.name}})
			}
			continue
		}
		if seen[a.name] {
			w.add(Finding{Line: a.line, Severity: Error, Key: "params.err.annotation_twice", Args: map[string]any{"annotation": "@" + a.name, "name": p.Name}})
			continue
		}
		seen[a.name] = true
		switch a.name {
		case "secret":
			p.Annotations.Secret = true
		case "required":
			p.Annotations.Required = true
		case "choices":
			p.Annotations.Choices, _ = choices(a.arg)
		case "pattern":
			p.Annotations.Pattern = a.arg
		case "fill":
			p.Annotations.Fill, fillLine = a.arg, a.line
		}
	}
	if p == nil {
		return
	}
	an := p.Annotations
	if an.Fill != "" {
		switch {
		case p.Bare:
			w.add(Finding{Line: fillLine, Severity: Error, Key: "params.err.fill_bare", Args: map[string]any{"name": p.Name}})
		case len(an.Choices) > 0:
			w.add(Finding{Line: fillLine, Severity: Error, Key: "params.err.fill_choices", Args: map[string]any{"name": p.Name}})
		}
		if first, taken := w.fills[an.Fill]; taken {
			w.add(Finding{Line: fillLine, Severity: Error, Key: "params.err.fill_twice", Args: map[string]any{"kind": an.Fill, "name": first}})
		} else {
			w.fills[an.Fill] = p.Name
		}
	}
	if p.Default != "" && len(an.Choices) > 0 && !slices.Contains(an.Choices, p.Default) {
		w.add(Finding{Line: p.Line, Severity: Error, Key: "params.err.default_choice", Args: map[string]any{"name": p.Name}})
	}
	if p.Default != "" && an.Pattern != "" && !regexp.MustCompile(anchored(an.Pattern)).MatchString(p.Default) {
		w.add(Finding{Line: p.Line, Severity: Warning, Key: "params.warn.default_pattern", Args: map[string]any{"name": p.Name}})
	}
}

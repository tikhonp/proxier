package params

import (
	"regexp"
	"strings"
)

var (
	startLine = regexp.MustCompile(`^#[ \t]*PARAMETERS[ \t]*$`)
	endLine   = regexp.MustCompile(`^#[ \t]*END[ \t]+PARAMETERS[ \t]*$`)
	localLine = regexp.MustCompile(`^[ \t]*:local(?:[ \t]|$)`)
	nameShape = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	bareShape = regexp.MustCompile(`^[A-Za-z0-9._:/+-]+$`)
)

// line is one line of a body without its terminator.
type line struct {
	text  string
	start int // byte offset of the line in the body
}

// splitLines splits after every '\n'; a line's "\r\n" or "\n" is its
// terminator, left out of text and kept in the body.
func splitLines(body []byte) []line {
	var out []line
	s := string(body)
	off := 0
	for off < len(s) {
		i := strings.IndexByte(s[off:], '\n')
		var t string
		next := len(s)
		if i < 0 {
			t = s[off:]
		} else {
			t = s[off : off+i]
			next = off + i + 1
		}
		out = append(out, line{text: strings.TrimSuffix(t, "\r"), start: off})
		off = next
	}
	return out
}

func isBlank(t string) bool   { return strings.TrimLeft(t, " \t") == "" }
func isComment(t string) bool { return strings.HasPrefix(strings.TrimLeft(t, " \t"), "#") }

// commentText is a comment line without its "#" and one following space.
func commentText(t string) string {
	t = strings.TrimPrefix(strings.TrimLeft(t, " \t"), "#")
	return strings.TrimPrefix(t, " ")
}

// isAnnotation reports a comment line whose text starts with "@".
func isAnnotation(t string) bool {
	return isComment(t) && strings.HasPrefix(strings.TrimLeft(commentText(t), " \t"), "@")
}

// Parse reads a body's PARAMETERS block. It never fails: what it can't read
// is a finding.
func Parse(body []byte) Script {
	lines := splitLines(body)
	var s Script
	start := -1
	for i, l := range lines {
		if startLine.MatchString(l.text) {
			start = i
			break
		}
	}
	for i, l := range lines {
		if (start < 0 || i < start) && endLine.MatchString(l.text) {
			s.Findings = append(s.Findings, Finding{Line: i + 1, Severity: Warning, Key: "params.warn.end_without_block", Args: map[string]any{"line": i + 1}})
		}
	}
	if start < 0 {
		s.Findings = append([]Finding{{Severity: Warning, Key: "params.warn.no_block"}}, s.Findings...)
		return s
	}
	s.Block, s.Start = true, start+1

	last := start // the last line of the block, as an index
	for i := start + 1; i < len(lines); i++ {
		if endLine.MatchString(lines[i].text) {
			s.Ended, s.End, last = true, i+1, i-1
			break
		}
	}
	if !s.Ended {
		i := start + 1
		for i < len(lines) && (isBlank(lines[i].text) || isComment(lines[i].text) || localLine.MatchString(lines[i].text)) {
			i++
		}
		last = i - 1
		s.End = last + 1
	}

	w := walker{s: &s, lines: lines, seen: map[string]bool{}, fills: map[string]string{}}
	for i := start + 1; i <= last; i++ {
		w.line(i)
	}
	w.orphans()
	w.flush()
	for _, g := range w.raw {
		items := make([]Item, 0, len(g))
		for _, r := range g {
			if r.param >= 0 {
				items = append(items, Item{Param: &s.Params[r.param]})
			} else {
				items = append(items, Item{Computed: &s.Computed[r.computed]})
			}
		}
		s.Groups = append(s.Groups, makeGroup(items))
	}

	if !s.Ended {
		names := w.names
		if len(names) > 3 {
			names = names[len(names)-3:]
		}
		s.Findings = append(s.Findings, Finding{Line: s.End, Severity: Warning, Key: "params.warn.no_end",
			Args: map[string]any{"line": s.End, "names": strings.Join(names, ", ")}})
	}
	sortFindings(s.Findings)
	return s
}

// walker reads the block's lines in order.
type walker struct {
	s     *Script
	lines []line
	run   []int // comment lines since the last blank, :local or code line
	group []ref
	raw   [][]ref
	names []string          // every :local name read, in order
	seen  map[string]bool   // names read, for duplicates
	fills map[string]string // @fill kind → the parameter holding it
}

func (w *walker) add(f Finding) { w.s.Findings = append(w.s.Findings, f) }

func (w *walker) line(i int) {
	t := w.lines[i].text
	n := i + 1
	switch {
	case isBlank(t):
		w.orphans()
		w.flush()
	case startLine.MatchString(t):
		w.add(Finding{Line: n, Severity: Error, Key: "params.err.second_block", Args: map[string]any{"line": n}})
		w.orphans()
	case isComment(t):
		w.run = append(w.run, i)
	case localLine.MatchString(t):
		w.local(i)
	default:
		w.add(Finding{Line: n, Severity: Warning, Key: "params.warn.code_in_block", Args: map[string]any{"line": n}})
		w.orphans()
	}
}

// orphans warns about the annotations of a run no :local line follows, and
// drops the run.
func (w *walker) orphans() {
	for _, i := range w.run {
		if a, ok := readAnnotation(w.lines[i].text, i+1); ok {
			if f := a.check(); f != nil {
				w.add(*f)
			}
			w.add(Finding{Line: i + 1, Severity: Warning, Key: "params.warn.annotation_orphan", Args: map[string]any{"annotation": "@" + a.name}})
		}
	}
	w.run = nil
}

// flush closes the current group.
func (w *walker) flush() {
	if len(w.group) == 0 {
		return
	}
	w.raw = append(w.raw, w.group)
	w.group = nil
}

// local reads a :local line: a parameter, a computed value, or an error.
func (w *walker) local(i int) {
	l := w.lines[i]
	n := i + 1
	run := w.run
	w.run = nil

	// the name starts after ":local" and its spaces
	pos := strings.Index(l.text, ":local") + len(":local")
	for pos < len(l.text) && (l.text[pos] == ' ' || l.text[pos] == '\t') {
		pos++
	}
	name := nameShape.FindString(l.text[pos:])
	after := pos + len(name)
	if name == "" || after < len(l.text) && l.text[after] != ' ' && l.text[after] != '\t' {
		w.add(Finding{Line: n, Severity: Error, Key: "params.err.unreadable", Args: map[string]any{"line": n}})
		w.annotationsOf(run, nil, false)
		return
	}
	// the value: the rest without the spaces around it
	vs := after
	for vs < len(l.text) && (l.text[vs] == ' ' || l.text[vs] == '\t') {
		vs++
	}
	expr := strings.TrimRight(l.text[vs:], " \t")

	var desc []string
	for _, j := range run {
		if !isAnnotation(w.lines[j].text) {
			if d := strings.TrimSpace(commentText(w.lines[j].text)); d != "" {
				desc = append(desc, d)
			}
		}
	}
	description := strings.Join(desc, " ")

	if w.seen[name] {
		w.add(Finding{Line: n, Severity: Error, Key: "params.err.duplicate", Args: map[string]any{"name": name}})
		w.annotationsOf(run, nil, false)
		return
	}
	w.seen[name] = true
	w.names = append(w.names, name)

	var p *Param
	switch {
	case strings.HasPrefix(expr, `"`):
		v, end, ok := unquote(expr)
		if !ok {
			w.add(Finding{Line: n, Severity: Error, Key: "params.err.unreadable", Args: map[string]any{"line": n}})
			w.annotationsOf(run, nil, false)
			return
		}
		if end == len(expr) {
			p = &Param{Name: name, Line: n, Default: v, Literal: expr, at: l.start + vs}
		}
	case bareShape.MatchString(expr):
		p = &Param{Name: name, Line: n, Default: expr, Literal: expr, Bare: true, at: l.start + vs}
	}
	if p == nil {
		c := Computed{Name: name, Line: n, Expr: expr, Description: description}
		w.s.Computed = append(w.s.Computed, c)
		w.group = append(w.group, ref{param: -1, computed: len(w.s.Computed) - 1})
		w.annotationsOf(run, nil, true)
		return
	}
	p.Description = description
	w.annotationsOf(run, p, false)
	w.s.Params = append(w.s.Params, *p)
	w.group = append(w.group, ref{param: len(w.s.Params) - 1, computed: -1})
}

// ref points into Script.Params or Script.Computed (-1 for neither) until
// the slices stop growing.
type ref struct{ param, computed int }

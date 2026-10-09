package ui

import (
	"bytes"
	"path"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// maxHighlight is the size above which a file is shown without colours: the
// view stays fast and nobody reads a megabyte of colour.
const maxHighlight = 512 << 10

// CodeLine is one line of a read-only code view: its number, the line as
// highlighted HTML (every character escaped by the formatter), and the
// findings that belong under it.
type CodeLine struct {
	N     int
	HTML  string
	Marks []CodeMark
}

// CodeMark is a validation finding shown under its line.
type CodeMark struct {
	Severity string // "error" | "warning"
	Text     string
}

// LangFor names the language of a file by its name: yaml, json, bash, nginx,
// routeros, or "" for plain text. A trailing ".template" is looked through, a
// ".conf" file is nginx (the only configuration language a template holds)
// and a ".rsc" file a RouterOS script (our own lexer, routeros.go).
func LangFor(name string) string {
	base := strings.ToLower(path.Base(name))
	base = strings.TrimSuffix(base, ".template")
	switch path.Ext(base) {
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".sh", ".bash":
		return "bash"
	case ".conf":
		return "nginx"
	case ".rsc":
		return "routeros"
	}
	return ""
}

var lineFormatter = html.New(html.WithClasses(true), html.PreventSurroundingPre(true))

// Highlight splits a file into lines with syntax colours as CSS classes
// (static/css/code.css gives them the palette's colours). Anything that is not
// a known language, or is too big, is escaped plain text.
func Highlight(name string, src []byte) []CodeLine {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" && len(src) == 0 {
		return nil
	}
	plain := func() []CodeLine {
		var out []CodeLine
		for i, l := range strings.Split(text, "\n") {
			out = append(out, CodeLine{N: i + 1, HTML: escapeHTML(l)})
		}
		return out
	}
	lang := LangFor(name)
	if lang == "" || len(src) > maxHighlight {
		return plain()
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return plain()
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return plain()
	}
	var out []CodeLine
	for i, toks := range chroma.SplitTokensIntoLines(it.Tokens()) {
		var buf bytes.Buffer
		if err := lineFormatter.Format(&buf, styles.Fallback, chroma.Literator(toks...)); err != nil {
			return plain()
		}
		out = append(out, CodeLine{N: i + 1, HTML: strings.TrimSuffix(buf.String(), "\n")})
	}
	return out
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

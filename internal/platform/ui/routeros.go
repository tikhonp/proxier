package ui

import (
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// init registers a small lexer for RouterOS scripts (.rsc), which chroma
// doesn't know. Its tokens give RP-Script's colours through app.css: a
// comment line is subtle italic, a "# @…" annotation iris (CommentSpecial,
// which no other lexer of ours emits), :commands foam italic, strings gold,
// $variables and numbers rose, a menu path at the start of a statement
// NameBuiltin.
func init() {
	lexers.Register(chroma.MustNewLexer(
		&chroma.Config{Name: "routeros", Aliases: []string{"rsc"}, Filenames: []string{"*.rsc"}},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{Pattern: `^[ \t]*#[ \t]*@[^\n]*`, Type: chroma.CommentSpecial},
					{Pattern: `^[ \t]*#[^\n]*`, Type: chroma.CommentSingle},
					{Pattern: `"`, Type: chroma.LiteralString, Mutator: chroma.Push("string")},
					{Pattern: `\$"[^"\n]*"`, Type: chroma.NameVariable},
					{Pattern: `\$[A-Za-z_][A-Za-z0-9_]*`, Type: chroma.NameVariable},
					{Pattern: `:[a-z][a-z-]*`, Type: chroma.NameAttribute},
					{Pattern: `^([ \t]*)(/[A-Za-z0-9/-]+)`, Type: chroma.ByGroups(chroma.Text, chroma.NameBuiltin)},
					{Pattern: `(\[)([ \t]*)(/[A-Za-z0-9/-]+)`, Type: chroma.ByGroups(chroma.Punctuation, chroma.Text, chroma.NameBuiltin)},
					{Pattern: `\b[0-9]+\b`, Type: chroma.LiteralNumber},
					{Pattern: `[=\[\](){};.]`, Type: chroma.Punctuation},
					{Pattern: `[A-Za-z_][A-Za-z0-9_-]*`, Type: chroma.Text},
					{Pattern: `\s+`, Type: chroma.Text},
					{Pattern: `.`, Type: chroma.Text},
				},
				"string": {
					{Pattern: `\\(?:[0-9A-F]{2}|.)`, Type: chroma.LiteralStringEscape},
					{Pattern: `\\\n`, Type: chroma.LiteralStringEscape},
					{Pattern: `[^"\\]+`, Type: chroma.LiteralString},
					{Pattern: `"`, Type: chroma.LiteralString, Mutator: chroma.Pop(1)},
				},
			}
		},
	))
}

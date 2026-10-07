package ui

import (
	"strconv"
	"strings"
)

// DiffLine is one line of a hunk: Kind is ' ' (context), '+' (added), '-'
// (removed) or '\\' (the "no newline" note).
type DiffLine struct {
	Kind     byte
	Old, New int // line numbers, 0 where the line is not on that side
	Text     string
}

// DiffHunk is one @@ block.
type DiffHunk struct {
	Header string
	Lines  []DiffLine
}

// SplitRow is one row of the side-by-side view; a zero Kind side is empty.
type SplitRow struct{ Left, Right DiffLine }

// DiffFile is one file of a diff. Note replaces the hunks for a binary file.
type DiffFile struct {
	Path   string
	Change string // added removed changed
	Note   string // already translated, e.g. "binary, 4 bytes → 6 bytes"
	Hunks  []DiffHunk
}

// DiffView is what Diff draws.
type DiffView struct {
	Split bool
	Files []DiffFile
}

// ParseUnified reads the hunks of a unified diff (as go-udiff writes it).
// The ---/+++ header lines are skipped.
func ParseUnified(s string) []DiffHunk {
	var hunks []DiffHunk
	var cur *DiffHunk
	oldN, newN := 0, 0
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			hunks = append(hunks, DiffHunk{Header: line})
			cur = &hunks[len(hunks)-1]
			oldN, newN = hunkStart(line)
		case cur == nil:
			// ---/+++ header
		case strings.HasPrefix(line, "+"):
			cur.Lines = append(cur.Lines, DiffLine{Kind: '+', New: newN, Text: line[1:]})
			newN++
		case strings.HasPrefix(line, "-"):
			cur.Lines = append(cur.Lines, DiffLine{Kind: '-', Old: oldN, Text: line[1:]})
			oldN++
		case strings.HasPrefix(line, `\`):
			cur.Lines = append(cur.Lines, DiffLine{Kind: '\\', Text: line})
		default:
			text := ""
			if len(line) > 0 {
				text = line[1:]
			}
			cur.Lines = append(cur.Lines, DiffLine{Kind: ' ', Old: oldN, New: newN, Text: text})
			oldN++
			newN++
		}
	}
	return hunks
}

// hunkStart reads "@@ -12,3 +14,4 @@" into the first old and new line numbers.
func hunkStart(h string) (oldStart, newStart int) {
	fields := strings.Fields(h)
	if len(fields) < 3 {
		return 1, 1
	}
	num := func(f string) int {
		f = strings.TrimLeft(f, "-+")
		f, _, _ = strings.Cut(f, ",")
		n, _ := strconv.Atoi(f)
		return n
	}
	return num(fields[1]), num(fields[2])
}

// Rows pairs a hunk's removed and added runs side by side: the first removed
// line against the first added one, and so on; the longer run is left open on
// the other side.
func (h DiffHunk) Rows() []SplitRow {
	var rows []SplitRow
	var minus, plus []DiffLine
	flush := func() {
		for i := 0; i < len(minus) || i < len(plus); i++ {
			var r SplitRow
			if i < len(minus) {
				r.Left = minus[i]
			}
			if i < len(plus) {
				r.Right = plus[i]
			}
			rows = append(rows, r)
		}
		minus, plus = nil, nil
	}
	for _, l := range h.Lines {
		switch l.Kind {
		case '-':
			minus = append(minus, l)
		case '+':
			plus = append(plus, l)
		case '\\':
			flush()
			rows = append(rows, SplitRow{Left: l, Right: l})
		default:
			flush()
			rows = append(rows, SplitRow{Left: l, Right: l})
		}
	}
	flush()
	return rows
}

func lineNo(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func marker(k byte) string {
	switch k {
	case '+':
		return "+"
	case '-':
		return "−"
	}
	return " "
}

func kindClass(k byte) string {
	switch k {
	case '+':
		return "add"
	case '-':
		return "del"
	case '\\':
		return "note"
	case ' ':
		return "ctx"
	}
	return "empty"
}

package templates

import (
	"bytes"
	"sort"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
)

// Change kinds of a FileDiff.
const (
	Added     = "added"
	Removed   = "removed"
	Changed   = "changed"
	Unchanged = "unchanged"
)

// FileDiff is one file compared between two file sets (versions, or a version
// and a draft).
type FileDiff struct {
	Path    string
	Change  string // added removed changed unchanged
	Unified string // "" for unchanged and for binary files
	Binary  bool
	// OldSize and NewSize are the byte sizes, 0 for the missing side.
	OldSize, NewSize int
}

// Diff compares two file sets file by file, sorted by path. A file that is
// not UTF-8 text (or holds a NUL) is binary: only its sizes are reported.
func Diff(a, b map[string][]byte) []FileDiff {
	paths := map[string]bool{}
	for p := range a {
		paths[p] = true
	}
	for p := range b {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	out := make([]FileDiff, 0, len(sorted))
	for _, p := range sorted {
		old, inA := a[p]
		cur, inB := b[p]
		d := FileDiff{Path: p, OldSize: len(old), NewSize: len(cur)}
		switch {
		case inA && !inB:
			d.Change = Removed
		case !inA && inB:
			d.Change = Added
		case bytes.Equal(old, cur):
			d.Change = Unchanged
		default:
			d.Change = Changed
		}
		if d.Change != Unchanged {
			if isBinary(old) || isBinary(cur) {
				d.Binary = true
			} else {
				d.Unified = udiff.Unified("a/"+p, "b/"+p, string(old), string(cur))
			}
		}
		out = append(out, d)
	}
	return out
}

func isBinary(b []byte) bool {
	return !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0
}

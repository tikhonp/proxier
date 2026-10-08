package templates

import (
	"path"
	"strings"
	"unicode/utf8"
)

// Limits of a draft, for the editor and the agent API alike.
const (
	MaxFileBytes  = 1 << 20  // one file
	MaxDraftBytes = 10 << 20 // all files together
	MaxPathLen    = 200
)

// ValidPath is a path a draft may hold: relative, clean, no ".." or ".git"
// segment, no empty segment, no backslash or NUL.
func ValidPath(p string) bool {
	if p == "" || len(p) > MaxPathLen || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || seg == ".git" {
			return false
		}
	}
	return path.Clean(p) == p && utf8.ValidString(p)
}

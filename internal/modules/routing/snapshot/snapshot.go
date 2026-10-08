// Package snapshot is pure: a service's names (Set), their canonical text and
// hash, and diffs between two sets. A snapshot is stored as text, never one
// row per name (build README, Phase 3).
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// Skipped is an upstream entry that was left out, and why.
type Skipped struct {
	Entry  string `json:"entry"`
	Reason string `json:"reason"` // i18n key: services.skip.unsupported, services.skip.invalid
}

// Set is what a service holds at a moment.
type Set struct {
	Suffix, Exact []string // sorted, unique; no exact name is also suffix
	Skipped       []Skipped
}

// New makes the canonical set: sorted, without duplicates, and without exact
// names that are also suffix. Nothing else is removed: an upstream service
// keeps names under its own suffixes (mtvpn compatibility).
func New(suffix, exact []string, skipped []Skipped) Set {
	s := Set{Suffix: uniq(suffix), Skipped: skipped}
	for _, e := range uniq(exact) {
		if _, found := slices.BinarySearch(s.Suffix, e); !found {
			s.Exact = append(s.Exact, e)
		}
	}
	return s
}

func uniq(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// Count is the number of names.
func (s Set) Count() int { return len(s.Suffix) + len(s.Exact) }

// Hash is SHA-256 hex of the canonical form; skipped entries don't count.
func (s Set) Hash() string {
	h := sha256.New()
	for _, n := range s.Suffix {
		_, _ = h.Write([]byte("s " + n + "\n"))
	}
	for _, n := range s.Exact {
		_, _ = h.Write([]byte("e " + n + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Has reports whether the set holds name in that form.
func (s Set) Has(name string, exact bool) bool {
	list := s.Suffix
	if exact {
		list = s.Exact
	}
	_, found := slices.BinarySearch(list, name)
	return found
}

// Encode is the stored text of sorted names.
func Encode(names []string) string { return strings.Join(names, "\n") }

// Decode reads what Encode wrote.
func Decode(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

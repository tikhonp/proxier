// Package mtvpn moves an mtvpn.yaml setup into Proxier once
// (docs/processes/routing/mtvpn-import.md): mtvpn's file rules, the preview
// (stored without the file's text) and the import job.
package mtvpn

import (
	"errors"
	"fmt"
	"strings"
)

// MaxFile is the largest mtvpn.yaml read.
const MaxFile = 64 << 10

// ErrTooBig: the file is over MaxFile.
var ErrTooBig = errors.New("mtvpn: the file is over 64 KiB")

// File is what the import reads of mtvpn.yaml. Every other key's value is
// dropped while parsing: credentials never leave Parse.
type File struct {
	Services     []string
	ServiceLists []string
	Base         string   // shadowrocket_base
	Ignored      []string // the other keys' names, in file order
}

// ParseError names the line only, never its text (it may hold a password).
type ParseError struct {
	Line   int
	NoKey  bool // a "- item" before any key
	Reason string
}

func (e *ParseError) Error() string {
	if e.NoKey {
		return fmt.Sprintf("line %d is a list item without a key", e.Line)
	}
	return fmt.Sprintf("line %d is not 'key: value'", e.Line)
}

var kept = map[string]bool{"services": true, "service_lists": true, "shadowrocket_base": true}

// unquote drops one pair of matching quotes.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// Parse follows mtvpn's parse_yaml: flat "key: value" scalars and
// one-level "- item" lists; blank lines and "#" lines are skipped.
func Parse(text string) (File, error) {
	if len(text) > MaxFile {
		return File{}, ErrTooBig
	}
	var f File
	key := ""
	seen := map[string]bool{}
	add := func(k, v string) {
		switch k {
		case "services":
			f.Services = append(f.Services, v)
		case "service_lists":
			f.ServiceLists = append(f.ServiceLists, v)
		case "shadowrocket_base":
			f.Base = v
		}
	}
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "-" || strings.HasPrefix(line, "- ") {
			if key == "" {
				return File{}, &ParseError{Line: i + 1, NoKey: true}
			}
			if v := unquote(strings.TrimSpace(strings.TrimPrefix(line, "-"))); v != "" {
				add(key, v)
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return File{}, &ParseError{Line: i + 1}
		}
		key = strings.TrimSpace(k)
		if !kept[key] && !seen[key] {
			f.Ignored = append(f.Ignored, key)
		}
		seen[key] = true
		if v = unquote(strings.TrimSpace(v)); v != "" {
			add(key, v)
		}
	}
	return f, nil
}

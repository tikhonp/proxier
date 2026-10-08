// Package output decides what a link serves: the format, which servers are
// hidden, the connection URIs or the one stub entry, and the headers
// (docs/processes/subscriptions/subscription-fetch.md, steps 3–6). It is pure:
// no database and no clock of its own, so every preview and the fetch get the
// same answer from the same inputs.
package output

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
)

// ErrBadFormat: a ?format= that is unknown or not allowed (the fetch's 400).
var ErrBadFormat = errors.New("output: format not allowed")

// Format renders the lines of a response.
type Format struct {
	Name   string
	Render func(lines []string) []byte
}

func plain(lines []string) []byte {
	if len(lines) == 0 {
		return []byte{}
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// formats in registry order: the order subscriptions store and pages show.
var formats = []Format{
	{Name: "uri-plain", Render: plain},
	{Name: "uri-base64", Render: func(lines []string) []byte {
		return []byte(base64.StdEncoding.EncodeToString(plain(lines)))
	}},
}

// Lookup finds a format by name.
func Lookup(name string) (Format, bool) {
	for _, f := range formats {
		if f.Name == name {
			return f, true
		}
	}
	return Format{}, false
}

// Names lists the formats in registry order.
func Names() []string {
	out := make([]string, len(formats))
	for i, f := range formats {
		out[i] = f.Name
	}
	return out
}

// PickFormat: the query's format when allowed; a non-empty query that is
// unknown or not allowed is ErrBadFormat; else the override when the
// subscription still allows it; else def.
func PickFormat(query string, allowed []string, def, override string) (string, error) {
	if query != "" {
		if _, ok := Lookup(query); !ok || !slices.Contains(allowed, query) {
			return "", ErrBadFormat
		}
		return query, nil
	}
	if override != "" && slices.Contains(allowed, override) {
		return override, nil
	}
	return def, nil
}

package sources

import (
	"context"
	"net/url"
	"strings"
)

// url resolves a list at an address; its includes are relative to its
// directory.
func (r *Resolver) url(ctx context.Context, raw string) (Resolved, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Resolved{}, err
	}
	dir := *u
	dir.RawQuery, dir.Fragment = "", ""
	if i := strings.LastIndexByte(dir.Path, '/'); i >= 0 {
		dir.Path = dir.Path[:i+1]
	}
	dir.RawPath = ""
	base := dir.String()
	ls := newLists(r.Fetch, func(name string) string {
		if name == "\x00root" {
			return raw
		}
		return base + url.PathEscape(name)
	})
	entries, err := ls.resolve(ctx, "\x00root")
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Set: set(entries, ls.skipped)}, nil
}

package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

// lists resolves list files with their includes: each list is fetched once
// per resolve, cycles are ignored, and the limits hold for the whole resolve.
type lists struct {
	f        *Fetcher
	url      func(name string) string // where a list (or an include) is
	done     map[string][]Entry
	active   map[string]bool
	skipped  []snapshot.Skipped
	fetched  int
	total    int64
	maxBytes int64
}

func newLists(f *Fetcher, url func(string) string) *lists {
	return &lists{f: f, url: url, done: map[string][]Entry{}, active: map[string]bool{}, maxBytes: MaxListBytes}
}

// resolve returns the entries of a list and of everything it includes.
func (ls *lists) resolve(ctx context.Context, name string) ([]Entry, error) {
	if e, ok := ls.done[name]; ok {
		return e, nil
	}
	if ls.fetched++; ls.fetched > MaxLists {
		return nil, fmt.Errorf("%w: more than %d lists", ErrTooBig, MaxLists)
	}
	u := ls.url(name)
	status, body, err := ls.f.Get(ctx, u, ls.maxBytes)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &HTTPError{URL: u, Status: status}
	}
	if ls.total += int64(len(body)); ls.total > MaxTotal {
		return nil, fmt.Errorf("%w: more than %d MiB in one resolve", ErrTooBig, MaxTotal>>20)
	}
	l := ParseList(string(body))
	ls.skipped = append(ls.skipped, l.Skipped...)
	ls.active[name] = true
	defer delete(ls.active, name)
	out := slices.Clone(l.Entries)
	for _, inc := range l.Includes {
		if ls.active[inc.Name] {
			continue // a cycle
		}
		sub, err := ls.resolve(ctx, inc.Name)
		if err != nil {
			var ie *IncludeError
			if errors.As(err, &ie) {
				return nil, err
			}
			return nil, &IncludeError{Name: inc.Name, Err: err}
		}
		for _, e := range sub {
			if inc.takes(e) {
				out = append(out, e)
			}
		}
	}
	ls.done[name] = out
	return out, nil
}

// takes reports whether the include's filter keeps the entry: every @a and
// no @-b.
func (inc Include) takes(e Entry) bool {
	for _, a := range inc.With {
		if !slices.Contains(e.Attrs, a) {
			return false
		}
	}
	for _, a := range inc.Without {
		if slices.Contains(e.Attrs, a) {
			return false
		}
	}
	return true
}

func (r *Resolver) v2fly(ctx context.Context, name string) (Resolved, error) {
	ls := newLists(r.Fetch, func(n string) string { return r.Endpoints.V2flyRaw + n })
	entries, err := ls.resolve(ctx, name)
	var he *HTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		var ie *IncludeError
		if !errors.As(err, &ie) {
			return Resolved{}, fmt.Errorf("%w: v2fly has no list '%s'", ErrNotFound, name)
		}
	}
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Set: set(entries, ls.skipped)}, nil
}

// Local resolves v2fly lists from files already parsed (the catalog's
// archive): includes with their filters, each list once, cycles ignored, no
// network. A missing include is an *IncludeError.
type Local struct {
	Files  map[string]List
	done   map[string][]Entry
	active map[string]bool
}

// Resolve returns the entries of a list and of everything it includes.
func (l *Local) Resolve(name string) ([]Entry, error) {
	if l.done == nil {
		l.done, l.active = map[string][]Entry{}, map[string]bool{}
	}
	if e, ok := l.done[name]; ok {
		return e, nil
	}
	f, ok := l.Files[name]
	if !ok {
		return nil, fmt.Errorf("%w: v2fly has no list '%s'", ErrNotFound, name)
	}
	l.active[name] = true
	defer delete(l.active, name)
	out := slices.Clone(f.Entries)
	for _, inc := range f.Includes {
		if l.active[inc.Name] {
			continue
		}
		sub, err := l.Resolve(inc.Name)
		if err != nil {
			var ie *IncludeError
			if errors.As(err, &ie) {
				return nil, err
			}
			return nil, &IncludeError{Name: inc.Name, Err: err}
		}
		for _, e := range sub {
			if inc.takes(e) {
				out = append(out, e)
			}
		}
	}
	l.done[name] = out
	return out, nil
}

// Count is the number of names entries make (a name in both forms counts once).
func Count(entries []Entry) int { return set(entries, nil).Count() }

// Filter is an include's filter as written: "@a @-b", "" for none.
func (inc Include) Filter() string {
	var parts []string
	for _, a := range inc.With {
		parts = append(parts, "@"+a)
	}
	for _, a := range inc.Without {
		parts = append(parts, "@-"+a)
	}
	return strings.Join(parts, " ")
}

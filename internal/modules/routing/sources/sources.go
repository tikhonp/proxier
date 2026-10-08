// Package sources resolves a selector into a snapshot.Set: v2fly lists with
// their includes and attribute filters, iplist sites and groups across the
// portals, and URL lists (docs/integrations/domain-sources.md). Every request
// goes through the one Fetcher; every base URL is in Endpoints, so tests
// point them at sourcestest.
package sources

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

// Portal is one iplist portal.
type Portal struct{ Name, Base string } // "beta", "https://beta.iplist.opencck.org/"

// Endpoints are every upstream base URL.
type Endpoints struct {
	V2flyRaw  string   // https://raw.githubusercontent.com/v2fly/domain-list-community/refs/heads/master/data/
	GitHubAPI string   // https://api.github.com/ (3c)
	Codeload  string   // https://codeload.github.com/ (3c)
	Portals   []Portal // main, beta, russia, in that order
}

// Production is where the real sources are.
func Production() Endpoints {
	return Endpoints{
		V2flyRaw:  "https://raw.githubusercontent.com/v2fly/domain-list-community/refs/heads/master/data/",
		GitHubAPI: "https://api.github.com/",
		Codeload:  "https://codeload.github.com/",
		Portals: []Portal{
			{Name: "main", Base: "https://iplist.opencck.org/"},
			{Name: "beta", Base: "https://beta.iplist.opencck.org/"},
			{Name: "russia", Base: "https://russia.iplist.opencck.org/"},
		},
	}
}

// What a resolve can fail with, besides *HTTPError, *UnreachableError,
// *IncludeError and transport errors.
var (
	// ErrNotFound: v2fly answered 404 for the list, or every iplist portal in
	// scope answered empty.
	ErrNotFound = errors.New("sources: not found")
	// ErrTooBig: a body, or a whole resolve, is over its limit.
	ErrTooBig = errors.New("sources: too big")
)

// HTTPError is an answer other than 200.
type HTTPError struct {
	URL    string
	Status int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.URL, e.Status) }

// UnreachableError: not found on the portals that answered, and some didn't.
// An unreachable portal is never "not found".
type UnreachableError struct {
	Missed      []string // portals that answered empty
	Unreachable []string // portals that didn't answer
}

func (e *UnreachableError) Error() string {
	var parts []string
	if len(e.Missed) > 0 {
		parts = append(parts, "not found on "+strings.Join(e.Missed, ", "))
	}
	return strings.Join(append(parts, strings.Join(e.Unreachable, ", ")+" unreachable"), "; ")
}

// IncludeError: an include failed, so the whole resolve did (a partial list
// is never used).
type IncludeError struct {
	Name string
	Err  error
}

func (e *IncludeError) Error() string {
	var he *HTTPError
	if errors.As(e.Err, &he) {
		return fmt.Sprintf("include:%s: HTTP %d", e.Name, he.Status)
	}
	return fmt.Sprintf("include:%s: %v", e.Name, e.Err)
}

func (e *IncludeError) Unwrap() error { return e.Err }

// Limits of one resolve.
const (
	MaxLists     = 300
	MaxTotal     = 16 << 20
	MaxListBytes = 8 << 20
)

// Resolved is a resolve's result.
type Resolved struct {
	Set    snapshot.Set
	Portal string // iplist: the portal it was found on
	Kind   string // iplist: site or group
}

// Resolver resolves selectors against Endpoints.
type Resolver struct {
	Endpoints Endpoints
	Fetch     *Fetcher
}

// Resolve fetches what the selector names. Nothing is cached.
func (r *Resolver) Resolve(ctx context.Context, s selector.Selector) (Resolved, error) {
	switch s.Source {
	case selector.V2fly:
		return r.v2fly(ctx, s.Name)
	case selector.Iplist:
		return r.iplist(ctx, s)
	case selector.URL:
		return r.url(ctx, s.URL)
	}
	return Resolved{}, fmt.Errorf("sources: nothing to resolve for %q", s.Source)
}

// ErrorText is a failure as stored and shown: short, with the status of an
// HTTP error rather than the whole address.
func ErrorText(err error) string {
	var he *HTTPError
	var ie *IncludeError
	text := err.Error()
	switch {
	case errors.As(err, &ie):
		text = ie.Error()
	case errors.As(err, &he):
		text = "HTTP " + strconv.Itoa(he.Status)
	case errors.Is(err, context.DeadlineExceeded):
		text = "timed out"
	case errors.Is(err, ErrNotFound):
		text = strings.TrimPrefix(text, ErrNotFound.Error()+": ")
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

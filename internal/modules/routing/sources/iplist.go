package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

// iplist tries the portals in order (or only the pinned one), the site before
// the group on each. An empty 200 is a miss; anything else that isn't a 200
// with names makes the portal unreachable for the rest of the resolve.
func (r *Resolver) iplist(ctx context.Context, s selector.Selector) (Resolved, error) {
	var missed, unreachable []string
	for _, p := range r.Endpoints.Portals {
		if s.Portal != "" && p.Name != s.Portal {
			continue
		}
		down := false
		for _, kind := range []string{"site", "group"} {
			u := p.Base + "?format=text&data=domains&wildcard=1&" + kind + "=" + url.QueryEscape(s.Name)
			status, body, err := r.Fetch.Get(ctx, u, MaxListBytes)
			if err != nil || status != http.StatusOK {
				down = true
				break
			}
			if strings.TrimSpace(string(body)) == "" {
				continue
			}
			return Resolved{Set: plainSet(string(body)), Portal: p.Name, Kind: kind}, nil
		}
		if down {
			unreachable = append(unreachable, p.Name)
		} else {
			missed = append(missed, p.Name)
		}
	}
	if len(unreachable) > 0 {
		return Resolved{}, &UnreachableError{Missed: missed, Unreachable: unreachable}
	}
	return Resolved{}, fmt.Errorf("%w: not found on %s", ErrNotFound, strings.Join(missed, ", "))
}

// plainSet reads iplist's output: one suffix name per line, scraped junk
// skipped.
func plainSet(body string) snapshot.Set {
	var names []string
	var skipped []snapshot.Skipped
	for _, line := range strings.Split(body, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if !domain.Valid(line) {
			skipped = append(skipped, snapshot.Skipped{Entry: line, Reason: SkipInvalid})
			continue
		}
		names = append(names, line)
	}
	return snapshot.New(names, nil, skipped)
}

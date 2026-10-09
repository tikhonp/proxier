package catalog

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// Suggestion is a catalog selector holding a website's names, for
// discovery's first section.
type Suggestion struct {
	Selector string
	Kind     string // list, site
	Portal   string // iplist
	Match    string // exact, suffix, above
	Name     string // the matching name in the list
	Domains  int
	Via      []string // v2fly lists holding it through an include
	Service  *services.Item
	Lists    []string
}

// How a suggestion matched.
const (
	MatchExact  = "exact"
	MatchSuffix = "suffix"
	MatchAbove  = "above"
)

// Lookup finds the catalog selectors of the current generations holding the
// host or the names between it and its registrable domain (exact or
// suffix), or a suffix above the registrable domain (above): one suggestion
// per selector, its best match; exact and suffix first, v2fly before
// iplist, smaller lists first. It reads only the local catalog.
func (s *Service) Lookup(ctx context.Context, host, registrable string) ([]Suggestion, error) {
	on := []string{host} // the site's own names: host … registrable
	for _, p := range domain.Parents(host) {
		if !domain.Covers(registrable, p) {
			break
		}
		on = append(on, p)
	}
	above := domain.Parents(registrable)
	rows, err := store.CatalogLookup(ctx, s.d.DB.R, append(slices.Clone(on), above...))
	if err != nil {
		return nil, err
	}
	var sites []string
	for _, r := range rows {
		if r.Source != "v2fly" {
			sites = append(sites, r.Name)
		}
	}
	sp, err := s.speller(ctx, sites)
	if err != nil {
		return nil, err
	}
	rank := map[string]int{MatchExact: 0, MatchSuffix: 1, MatchAbove: 2}
	best := map[string]Suggestion{}
	attrs := map[string]string{}
	for _, r := range rows {
		var match string
		switch {
		case slices.Contains(on, r.Domain) && r.Exact:
			match = MatchExact
		case slices.Contains(on, r.Domain):
			match = MatchSuffix
		case !r.Exact:
			match = MatchAbove
		default:
			continue // an exact name above the site holds none of it
		}
		sg := Suggestion{Kind: "list", Match: match, Name: r.Domain, Domains: r.Domains}
		if r.Source == "v2fly" {
			sg.Selector = "v2fly:" + r.Name
		} else {
			sg.Kind, sg.Portal = "site", portalOf(r.Source)
			sg.Selector = sp.spell(sg.Portal, r.Name)
		}
		if b, ok := best[sg.Selector]; ok && rank[b.Match] <= rank[match] {
			continue
		}
		best[sg.Selector] = sg
		attrs[sg.Selector] = r.Attrs
	}
	svcs, err := s.d.Services.List(ctx, services.Filter{})
	if err != nil {
		return nil, err
	}
	bySel := map[string]services.Row{}
	for _, r := range svcs {
		if r.Selector != "" {
			bySel[r.Selector] = r
		}
	}
	out := make([]Suggestion, 0, len(best))
	for sel, sg := range best {
		if sg.Kind == "list" {
			if sg.Via, err = s.includers(ctx, strings.TrimPrefix(sel, "v2fly:"), attrs[sel]); err != nil {
				return nil, err
			}
		}
		if row, ok := bySel[sel]; ok {
			it := row.Item
			sg.Service, sg.Lists = &it, row.Lists
		}
		out = append(out, sg)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Match == MatchAbove) != (b.Match == MatchAbove) {
			return b.Match == MatchAbove
		}
		if (a.Kind == "list") != (b.Kind == "list") {
			return a.Kind == "list"
		}
		if a.Domains != b.Domains {
			return a.Domains < b.Domains
		}
		return a.Selector < b.Selector
	})
	return out, nil
}

// includers are the v2fly lists that take an entry with these attributes
// ("@ads @cn") from list through includes, every filter on the way applied,
// by name.
func (s *Service) includers(ctx context.Context, list, attrs string) ([]string, error) {
	have := strings.Fields(attrs)
	seen := map[string]bool{list: true}
	next := []string{list}
	var out []string
	for len(next) > 0 {
		rows, err := store.V2flyIncluders(ctx, s.d.DB.R, next)
		if err != nil {
			return nil, err
		}
		next = nil
		for _, r := range rows {
			if seen[r.List] || !takes(r.Filter, have) {
				continue
			}
			seen[r.List] = true
			out = append(out, r.List)
			next = append(next, r.List)
		}
	}
	sort.Strings(out)
	return out, nil
}

// takes reports whether an include's filter ("@a @-b") keeps an entry with
// these attributes ("@a"), as sources resolves it.
func takes(filter string, attrs []string) bool {
	for _, f := range strings.Fields(filter) {
		if a, ok := strings.CutPrefix(f, "@-"); ok {
			if slices.Contains(attrs, "@"+a) {
				return false
			}
		} else if !slices.Contains(attrs, f) {
			return false
		}
	}
	return true
}

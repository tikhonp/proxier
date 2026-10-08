package catalog

import (
	"context"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// MaxResults is how many results a search shows at most.
const MaxResults = 200

// Search finds catalog entries whose name holds the query, case-insensitive,
// in the current generations: v2fly lists first, then iplist groups, then
// sites, each by name. more is how many matched beyond the limit. An empty
// query finds nothing. Search reads only the local catalog: it works while
// the sources are down.
func (s *Service) Search(ctx context.Context, q Query) (results []Result, more int, err error) {
	text := strings.ToLower(strings.TrimSpace(q.Q))
	if text == "" {
		return nil, 0, nil
	}
	limit := q.Limit
	if limit <= 0 || limit > MaxResults {
		limit = MaxResults
	}
	src := q.Source
	if src != "v2fly" && src != "iplist" {
		src = ""
	}
	kind := q.Kind
	if kind != "list" && kind != "site" && kind != "group" {
		kind = ""
	}
	portal := ""
	for _, p := range Sources[1:] {
		if q.Portal == portalOf(p) {
			portal = q.Portal
		}
	}
	rows, total, err := store.CatalogSearch(ctx, s.d.DB.R, text, src, kind, portal, limit)
	if err != nil {
		return nil, 0, err
	}
	var iplistNames []string
	for _, r := range rows {
		if r.Source != "v2fly" {
			iplistNames = append(iplistNames, r.Name)
		}
	}
	sp, err := s.speller(ctx, iplistNames)
	if err != nil {
		return nil, 0, err
	}
	svcs, err := s.d.Services.List(ctx, services.Filter{})
	if err != nil {
		return nil, 0, err
	}
	bySel := map[string]services.Row{}
	for _, r := range svcs {
		if r.Selector != "" {
			bySel[r.Selector] = r
		}
	}
	now := s.d.Now()
	for _, r := range rows {
		res := Result{
			Entry:      Entry{Source: r.Source, Kind: r.Kind, Name: r.Name, Group: r.Group, Sites: r.Sites, Domains: r.Domains},
			Selectable: true,
		}
		if r.Source == "v2fly" {
			res.Selector = "v2fly:" + r.Name
		} else {
			res.Portal = portalOf(r.Source)
			res.Selector = sp.spell(res.Portal, r.Name)
			res.Selectable = sp.selectable(res.Portal, r.Kind, r.Name)
		}
		if !r.RefreshedAt.IsZero() {
			res.Age = now.Sub(r.RefreshedAt.Time)
		}
		if row, ok := bySel[res.Selector]; ok && res.Selectable {
			it := row.Item
			res.Service, res.Lists = &it, row.Lists
		}
		results = append(results, res)
	}
	return results, total - len(results), nil
}

// Counts are the entries of each source's current catalog (v2fly, iplist:…).
func (s *Service) Counts(ctx context.Context) (map[string]int, error) {
	return store.CatalogCounts(ctx, s.d.DB.R)
}

// Matches counts the query's matches per source family, for the chips.
func (s *Service) Matches(ctx context.Context, q Query) (v2fly, iplist int, err error) {
	text := strings.ToLower(strings.TrimSpace(q.Q))
	if text == "" {
		return 0, 0, nil
	}
	if _, v2fly, err = store.CatalogSearch(ctx, s.d.DB.R, text, "v2fly", "", "", 1); err != nil {
		return 0, 0, err
	}
	_, iplist, err = store.CatalogSearch(ctx, s.d.DB.R, text, "iplist", "", "", 1)
	return v2fly, iplist, err
}

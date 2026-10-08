package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// exportMax bounds one portal's custom export.
const exportMax = 16 << 20

// exportTemplate prints one line per domain: its group, its site, the domain.
const exportTemplate = "{group}|{site}|{data}"

var iplistName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,252}$`)

// fetchIplist downloads a portal's custom export and builds its generation;
// an export equal to the one in force (by SHA-256) is errUnchanged.
func (s *Service) fetchIplist(ctx context.Context, p sources.Portal, cur store.CatalogSource) (generation, error) {
	u := p.Base + "?format=custom&data=domains&wildcard=1&template=" + url.QueryEscape(exportTemplate)
	status, body, err := s.d.Fetch.Get(ctx, u, exportMax)
	if err != nil {
		return generation{}, err
	}
	if status != http.StatusOK {
		return generation{}, &sources.HTTPError{URL: u, Status: status}
	}
	sum := sha256.Sum256(body)
	rev := hex.EncodeToString(sum[:])
	if rev == cur.Revision && cur.Generation > 0 {
		return generation{}, errUnchanged
	}
	g := parseExport("iplist:"+p.Name, string(body))
	g.revision = rev
	return g, nil
}

// parseExport reads group|site|domain lines (split on the first two |):
// a group entry per group with its sites and names, a site entry per site
// with its group and names, a reverse-index row per (site, domain) as suffix.
// Malformed lines and invalid domains are skipped.
func parseExport(source, body string) generation {
	type site struct {
		group   string
		domains map[string]bool
	}
	sites := map[string]*site{}
	groups := map[string]map[string]bool{} // group → sites
	var order []string
	for _, line := range strings.Split(body, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 {
			continue
		}
		g, sn, d := strings.ToLower(strings.TrimSpace(parts[0])), strings.ToLower(strings.TrimSpace(parts[1])), strings.ToLower(strings.TrimSpace(parts[2]))
		if !iplistName.MatchString(g) || !iplistName.MatchString(sn) || !domain.Valid(d) {
			continue
		}
		st := sites[sn]
		if st == nil {
			st = &site{group: g, domains: map[string]bool{}}
			sites[sn] = st
			order = append(order, sn)
		}
		st.domains[d] = true
		if groups[g] == nil {
			groups[g] = map[string]bool{}
		}
		groups[g][sn] = true
	}
	gen := generation{source: source}
	for g, ss := range groups {
		names := map[string]bool{}
		for sn := range ss {
			for d := range sites[sn].domains {
				names[d] = true
			}
		}
		gen.entries = append(gen.entries, store.CatalogEntry{Source: source, Kind: "group", Name: g, Sites: len(ss), Domains: len(names)})
	}
	for _, sn := range order {
		st := sites[sn]
		gen.entries = append(gen.entries, store.CatalogEntry{Source: source, Kind: "site", Name: sn, Group: st.group, Domains: len(st.domains)})
		for d := range st.domains {
			gen.domains = append(gen.domains, store.CatalogDomain{Source: source, Name: sn, Domain: d})
		}
	}
	return gen
}

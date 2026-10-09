package shadowrocket

import (
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
)

// Rules are a list's blocks for Shadowrocket: each service's owned names in
// list order, minus every name another rule of the config covers (mtvpn's
// shadowrocket_rules): a suffix name under any suffix name of the config,
// an exact name equal to or under one. Unlike on routers this applies inside
// a service too, since Shadowrocket's suffix rule already matches them. A
// service left with nothing gets no block.
func Rules(v lists.View) []Block {
	suffixes := map[string]bool{}
	for _, o := range v.Result.Services {
		for _, n := range o.Suffix {
			suffixes[n] = true
		}
	}
	under := func(n string) bool {
		for _, p := range domain.Parents(n) {
			if suffixes[p] {
				return true
			}
		}
		return false
	}
	var out []Block
	for _, o := range v.Result.Services {
		b := Block{Tag: o.Tag}
		for _, n := range o.Suffix {
			if !under(n) {
				b.Rules = append(b.Rules, Rule{Name: n})
			}
		}
		for _, n := range o.Exact {
			if !suffixes[n] && !under(n) {
				b.Rules = append(b.Rules, Rule{Name: n, Exact: true})
			}
		}
		if len(b.Rules) > 0 {
			out = append(out, b)
		}
	}
	return out
}

// Count is the number of rules of the blocks.
func Count(blocks []Block) int {
	n := 0
	for _, b := range blocks {
		n += len(b.Rules)
	}
	return n
}

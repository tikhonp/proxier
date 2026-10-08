package own

import "github.com/tikhonp/proxier/internal/modules/routing/domain"

// Cover is another service that holds a name, or a suffix above it.
type Cover struct {
	Tag   string // the other service
	Via   string // its name that covers (the same name or a suffix above)
	Exact bool
}

// CoveredBy lists the other members (all but except) that hold the name in
// the same form, or a suffix above it, in member order: the custom editor's
// "covered by openai in Main" hints.
func CoveredBy(name string, exact bool, members []Member, except int64) []Cover {
	var out []Cover
	for _, m := range members {
		if m.ServiceID == except {
			continue
		}
		switch {
		case m.Set.Has(name, false):
			out = append(out, Cover{Tag: m.Tag, Via: name})
			continue
		case exact && m.Set.Has(name, true):
			out = append(out, Cover{Tag: m.Tag, Via: name, Exact: true})
			continue
		}
		parents := domain.Parents(name)
		for k := len(parents) - 1; k >= 0; k-- { // the broadest first
			if m.Set.Has(parents[k], false) {
				out = append(out, Cover{Tag: m.Tag, Via: parents[k]})
				break
			}
		}
	}
	return out
}

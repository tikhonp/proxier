// Package own is pure: ownership within a routing list (ADR 0013), the
// server-hostname guard and "covered by". Nothing here is stored: a page, a
// sync, a preview or a fetch computes it when it needs it (build README,
// Phase 3: "Ownership is computed, never stored").
package own

import (
	"slices"
	"sort"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

// Member is a service of a list, in list order, with its accepted snapshot.
type Member struct {
	ServiceID int64
	Tag       string
	Set       snapshot.Set
}

// Server is a server whose hostnames no list may cover.
type Server struct {
	Name      string
	Hostnames []string
}

// Why a name of a service isn't installed under its tag.
const (
	ReasonCovered = "covered" // another service has it, or a suffix above it, as a suffix
	ReasonOwned   = "owned"   // an earlier service has it in the same form
	ReasonGuarded = "guarded" // it covers a server's hostname
	ReasonPinned  = "pinned"  // excluded by the caller (a router's infra pins, 3e)
)

// Drop is a name a service lists but doesn't install.
type Drop struct {
	Name   string
	Exact  bool
	Reason string // ReasonCovered, ReasonOwned, ReasonGuarded, ReasonPinned
	By     string // the covering or owning tag; the server's name for Guarded
	Via    string // the covering suffix; the hostname for ReasonGuarded; the reason text for ReasonPinned
}

// Owned is what one service of a list installs.
type Owned struct {
	ServiceID     int64
	Tag           string
	Suffix, Exact []string // what targets install under the tag, sorted
	Total         int      // names in its snapshot
	Dropped       []Drop   // by name
}

// Count is the number of names the service installs.
func (o Owned) Count() int { return len(o.Suffix) + len(o.Exact) }

// Result is a list's ownership.
type Result struct {
	Services []Owned // in list order, one per member
}

// Count is the number of names the list installs, all services.
func (r Result) Count() int {
	n := 0
	for _, o := range r.Services {
		n += o.Count()
	}
	return n
}

// Guarded lists the names left out for a server's hostname, in list order.
func (r Result) Guarded() []GuardedName {
	var out []GuardedName
	for _, o := range r.Services {
		for _, d := range o.Dropped {
			if d.Reason == ReasonGuarded {
				out = append(out, GuardedName{Tag: o.Tag, ServiceID: o.ServiceID, Drop: d})
			}
		}
	}
	return out
}

// GuardedName is a name left out of a list because it covers a server's hostname.
type GuardedName struct {
	ServiceID int64
	Tag       string
	Drop
}

// Compute applies the guard, the exclusions (name → reason text) and ADR 0013:
//
//  1. Names that cover a server's hostname, or are excluded, are taken out of
//     every set first: they are installed by nobody and cover nothing.
//  2. A suffix name is dropped as covered when another service has a proper
//     parent of it as a suffix; else as owned when an earlier service has it
//     as a suffix.
//  3. An exact name is dropped as covered when another service has it, or a
//     parent of it, as a suffix; else as owned when an earlier service has it
//     as an exact name.
//
// Names under a service's own suffixes stay (mtvpn compatibility).
func Compute(members []Member, servers []Server, exclude map[string]string) Result {
	type entry struct {
		suffix, exact []string
	}
	kept := make([]entry, len(members))
	res := Result{Services: make([]Owned, len(members))}
	// where each suffix and exact name is, in list order
	suffixAt := map[string][]int{}
	for i, m := range members {
		o := Owned{ServiceID: m.ServiceID, Tag: m.Tag, Total: m.Set.Count()}
		take := func(names []string, exact bool) []string {
			var out []string
			for _, n := range names {
				if why, ok := exclude[n]; ok {
					o.Dropped = append(o.Dropped, Drop{Name: n, Exact: exact, Reason: ReasonPinned, Via: why})
					continue
				}
				if srv, host, ok := Covering(n, exact, servers); ok {
					o.Dropped = append(o.Dropped, Drop{Name: n, Exact: exact, Reason: ReasonGuarded, By: srv, Via: host})
					continue
				}
				out = append(out, n)
			}
			return out
		}
		kept[i] = entry{suffix: take(m.Set.Suffix, false), exact: take(m.Set.Exact, true)}
		for _, n := range kept[i].suffix {
			suffixAt[n] = append(suffixAt[n], i)
		}
		res.Services[i] = o
	}
	// other is the first service other than i holding name as a suffix.
	other := func(name string, i int) (int, bool) {
		for _, j := range suffixAt[name] {
			if j != i {
				return j, true
			}
		}
		return 0, false
	}
	// cover is the broadest suffix of another service above name (or name
	// itself when self is true), and the service that has it.
	cover := func(name string, i int, self bool) (string, int, bool) {
		via, by, ok := "", 0, false
		if self {
			if j, found := other(name, i); found {
				via, by, ok = name, j, true
			}
		}
		for _, p := range domain.Parents(name) {
			if j, found := other(p, i); found {
				via, by, ok = p, j, true // keep going: the broadest wins
			}
		}
		return via, by, ok
	}
	// the first service that installs a name owns it
	ownerS, ownerE := map[string]string{}, map[string]string{}
	for i := range members {
		o := &res.Services[i]
		for _, n := range kept[i].suffix {
			if via, j, ok := cover(n, i, false); ok {
				o.Dropped = append(o.Dropped, Drop{Name: n, Reason: ReasonCovered, By: members[j].Tag, Via: via})
				continue
			}
			if by, ok := ownerS[n]; ok {
				o.Dropped = append(o.Dropped, Drop{Name: n, Reason: ReasonOwned, By: by})
				continue
			}
			ownerS[n] = o.Tag
			o.Suffix = append(o.Suffix, n)
		}
		for _, n := range kept[i].exact {
			if via, j, ok := cover(n, i, true); ok {
				o.Dropped = append(o.Dropped, Drop{Name: n, Exact: true, Reason: ReasonCovered, By: members[j].Tag, Via: via})
				continue
			}
			if by, ok := ownerE[n]; ok {
				o.Dropped = append(o.Dropped, Drop{Name: n, Exact: true, Reason: ReasonOwned, By: by})
				continue
			}
			ownerE[n] = o.Tag
			o.Exact = append(o.Exact, n)
		}
		sort.Strings(o.Suffix)
		sort.Strings(o.Exact)
		slices.SortFunc(o.Dropped, func(a, b Drop) int {
			if c := strings.Compare(a.Name, b.Name); c != 0 {
				return c
			}
			if a.Exact == b.Exact {
				return 0
			}
			if a.Exact {
				return 1
			}
			return -1
		})
	}
	return res
}

// Move is a name whose owner changed.
type Move struct {
	Name     string
	Exact    bool
	From, To string // tags; "" when not installed
}

type key struct {
	name  string
	exact bool
}

func owners(r Result) map[key]string {
	m := map[key]string{}
	for _, o := range r.Services {
		for _, n := range o.Suffix {
			m[key{n, false}] = o.Tag
		}
		for _, n := range o.Exact {
			m[key{n, true}] = o.Tag
		}
	}
	return m
}

// Moved lists the names whose owner differs between two results of one list
// (a reorder's "14 domains change owner"), by name.
func Moved(before, after Result) []Move {
	a, b := owners(before), owners(after)
	var out []Move
	for k, from := range a {
		if to := b[k]; to != from {
			out = append(out, Move{Name: k.name, Exact: k.exact, From: from, To: to})
		}
	}
	for k, to := range b {
		if _, ok := a[k]; !ok {
			out = append(out, Move{Name: k.name, Exact: k.exact, To: to})
		}
	}
	slices.SortFunc(out, func(x, y Move) int {
		if c := strings.Compare(x.Name, y.Name); c != 0 {
			return c
		}
		if x.Exact == y.Exact {
			return 0
		}
		if x.Exact {
			return 1
		}
		return -1
	})
	return out
}

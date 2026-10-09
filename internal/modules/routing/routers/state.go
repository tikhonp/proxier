package routers

import (
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
)

// State is what a read found on the router, grouped by tag.
type State struct {
	Tags     map[string]*TagState
	Untagged int               // DNS and address-list entries with no comment
	Pins     map[string]string // name → its mtvpn: comment
	ReadAt   time.Time
	Names    routeros.Names // what was read: the forwarder entries must point at
}

// TagState is a tag's entries on the router.
type TagState struct {
	DNS  []routeros.DNSEntry
	List []string // addresses
}

// Entries counts the tag's DNS entries.
func (t *TagState) Entries() int {
	if t == nil {
		return 0
	}
	return len(t.DNS)
}

func (t *TagState) empty() bool { return t == nil || len(t.DNS)+len(t.List) == 0 }

// Interpret groups a read. Tags come from the DNS entries' comments, so an
// address-list-only comment (telegram-cidr) is never a tag; an empty comment
// is untagged; an address-list entry whose comment starts with mtvpn: is an
// infra pin; a tag's address-list entries are attached to it by comment.
func Interpret(dns []routeros.DNSEntry, list []routeros.ListEntry, n routeros.Names, at time.Time) State {
	st := State{Tags: map[string]*TagState{}, Pins: map[string]string{}, ReadAt: at, Names: n}
	for _, d := range dns {
		switch {
		case d.Comment == "":
			st.Untagged++
		case strings.HasPrefix(d.Comment, routeros.InfraPrefix):
		default:
			t := st.Tags[d.Comment]
			if t == nil {
				t = &TagState{}
				st.Tags[d.Comment] = t
			}
			t.DNS = append(t.DNS, d)
		}
	}
	for _, e := range list {
		switch {
		case e.Comment == "":
			st.Untagged++
		case strings.HasPrefix(e.Comment, routeros.InfraPrefix):
			st.Pins[e.Address] = e.Comment
		default:
			if t := st.Tags[e.Comment]; t != nil {
				t.List = append(t.List, e.Address)
			}
		}
	}
	return st
}

// Matches is the strict comparison: the tag's DNS entries' (name,
// match-subdomain) multiset equals the desired (name, !exact) set, every one
// is FWD to the forwarder, and its address-list addresses equal the desired
// names as a multiset.
func Matches(t *TagState, want []routeros.Entry, n routeros.Names) bool {
	if t == nil {
		return len(want) == 0
	}
	if len(t.DNS) != len(want) || len(t.List) != len(want) {
		return false
	}
	type key struct {
		name string
		sub  bool
	}
	need := map[key]int{}
	names := map[string]int{}
	for _, e := range want {
		need[key{e.Name, !e.Exact}]++
		names[e.Name]++
	}
	for _, d := range t.DNS {
		if d.Type != "FWD" || d.ForwardTo != n.Forwarder {
			return false
		}
		k := key{d.Name, d.Subdomain}
		if need[k] == 0 {
			return false
		}
		need[k]--
	}
	for _, a := range t.List {
		if names[a] == 0 {
			return false
		}
		names[a]--
	}
	return true
}

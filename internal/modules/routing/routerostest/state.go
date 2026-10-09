package routerostest

import (
	"slices"
	"sort"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
)

// dnsRow is a DNS static entry; List is its address-list= field.
type dnsRow struct {
	routeros.DNSEntry
	List string
}

// listRow is an address-list entry.
type listRow struct {
	List, Address, Comment string
	Dynamic                bool
}

// apply runs one op with RouterOS's semantics, as the router would run the
// block. The caller holds r.mu.
func (r *Router) apply(op routeros.Op) {
	l := op.Names.List
	switch op.Kind {
	case "remove":
		r.removeTag(l, op.Tag)
		return
	case "update":
		r.removeTag(l, op.Tag)
	}
	ours := map[string]bool{}
	for _, e := range op.Entries {
		ours[e.Name] = true
	}
	r.dns = slices.DeleteFunc(r.dns, func(d dnsRow) bool { return d.List == l && d.Type == "FWD" && ours[d.Name] })
	r.list = slices.DeleteFunc(r.list, func(e listRow) bool {
		return e.List == l && !e.Dynamic && ours[e.Address] && !strings.HasPrefix(e.Comment, routeros.InfraPrefix)
	})
	for _, e := range op.Entries {
		r.dns = append(r.dns, dnsRow{List: l, DNSEntry: routeros.DNSEntry{
			Comment: op.Tag, Name: e.Name, Type: "FWD", ForwardTo: op.Names.Forwarder, Subdomain: !e.Exact,
		}})
		if r.dropAdds[op.Tag] > 0 {
			r.dropAdds[op.Tag]--
			continue
		}
		if slices.ContainsFunc(r.list, func(x listRow) bool { return x.List == l && x.Address == e.Name }) {
			continue // :do {…} on-error={}: a duplicate address fails silently
		}
		r.list = append(r.list, listRow{List: l, Address: e.Name, Comment: op.Tag})
	}
}

func (r *Router) removeTag(list, tag string) {
	r.dns = slices.DeleteFunc(r.dns, func(d dnsRow) bool { return d.List == list && d.Comment == tag })
	r.list = slices.DeleteFunc(r.list, func(e listRow) bool { return e.List == list && e.Comment == tag && !e.Dynamic })
}

// Tag is a tag's DNS and address-list entries in the default list.
func (r *Router) Tag(tag string) (dns []routeros.DNSEntry, list []routeros.ListEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.dns {
		if d.List == routeros.Defaults.List && d.Comment == tag {
			dns = append(dns, d.DNSEntry)
		}
	}
	for _, e := range r.list {
		if e.List == routeros.Defaults.List && e.Comment == tag && !e.Dynamic {
			list = append(list, routeros.ListEntry{Comment: e.Comment, Address: e.Address})
		}
	}
	return dns, list
}

// Names are a tag's DNS names, sorted.
func (r *Router) Names(tag string) []string {
	dns, _ := r.Tag(tag)
	out := make([]string, 0, len(dns))
	for _, d := range dns {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}

// Tags are the DNS entries' comments in the default list, sorted; untagged
// entries aren't a tag.
func (r *Router) Tags() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, d := range r.dns {
		if d.List == routeros.Defaults.List && d.Comment != "" {
			seen[d.Comment] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Untagged counts the DNS and static address-list entries with no comment.
func (r *Router) Untagged() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.dns {
		if d.List == routeros.Defaults.List && d.Comment == "" {
			n++
		}
	}
	for _, e := range r.list {
		if e.List == routeros.Defaults.List && e.Comment == "" && !e.Dynamic {
			n++
		}
	}
	return n
}

// Holds reports whether name is in the address list or has a DNS entry.
func (r *Router) Holds(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.dns {
		if d.Name == name {
			return true
		}
	}
	return false
}

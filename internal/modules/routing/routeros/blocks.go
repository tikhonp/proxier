package routeros

import "fmt"

// Block is what one tag needs pushed: its entries (an update) or its removal.
type Block struct {
	Tag     string
	Remove  bool    // a removal block
	Entries []Entry // an update
}

// ServiceBlock installs a tag's entries (routeros.md#service-block): it
// removes the tag's own entries (unless continued: a later part of a split
// tag, which must not undo the earlier parts), then any other entry for the
// same names except mtvpn: pins (adoption), then adds every name tagged. One
// :set per line: the console caps line length. Suffix names first, sorted,
// then exact ones.
func ServiceBlock(n Names, tag string, entries []Entry, continued bool) []string {
	e := append([]Entry(nil), entries...)
	Sort(e)
	out := []string{"{"}
	if !continued {
		out = append(out, RemovalBlock(n, tag)...)
	}
	out = append(out, `:local ours [:toarray ""]`)
	for _, x := range e {
		out = append(out, fmt.Sprintf(`:set ($ours->"%s") 1`, x.Name))
	}
	// One print instead of a get per entry, as in the read: with 3,226
	// names on a CHR, the two loops took 2.9 s instead of 13.4 s.
	out = append(out,
		fmt.Sprintf(`:foreach e in=[/ip dns static print as-value proplist=name where address-list="%s" type=FWD] do={:if ([:typeof ($ours->($e->"name"))]!="nothing") do={/ip dns static remove ($e->".id")}}`, n.List),
		fmt.Sprintf(`:foreach e in=[/ip firewall address-list print as-value proplist=address,comment where list="%s" dynamic=no] do={:if ([:typeof ($ours->($e->"address"))]!="nothing" && [:pick [:tostr ($e->"comment")] 0 6]!="mtvpn:") do={/ip firewall address-list remove ($e->".id")}}`, n.List),
	)
	for _, x := range e {
		sub := "yes"
		if x.Exact {
			sub = "no"
		}
		out = append(out, fmt.Sprintf(`/ip dns static add name=%s type=FWD match-subdomain=%s forward-to=%s address-list=%s comment="%s"`, x.Name, sub, n.Forwarder, n.List, Quote(tag)))
	}
	// The router starts resolving a hostname entry once it's added, and
	// every /ip dns command waits on those lookups: on a CHR, 678 names
	// imported in 24 s with the address-list entries last, and in 321 s
	// with each one after its DNS entry.
	for _, x := range e {
		out = append(out, fmt.Sprintf(`:do {/ip firewall address-list add list=%s address=%s comment="%s"} on-error={}`, n.List, x.Name, Quote(tag)))
	}
	return append(out, "}")
}

// RemovalBlock removes a tag's entries: its DNS entries in the address list
// and its static address-list entries.
func RemovalBlock(n Names, tag string) []string {
	return []string{
		fmt.Sprintf(`/ip dns static remove [find comment="%s" address-list="%s"]`, Quote(tag), n.List),
		fmt.Sprintf(`/ip firewall address-list remove [find list="%s" comment="%s" dynamic=no]`, n.List, Quote(tag)),
	}
}

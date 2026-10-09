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
	out = append(out,
		fmt.Sprintf(`:foreach e in=[/ip dns static find where address-list="%s" type=FWD] do={:if ([:typeof ($ours->[/ip dns static get $e name])]!="nothing") do={/ip dns static remove $e}}`, n.List),
		fmt.Sprintf(`:foreach e in=[/ip firewall address-list find where list="%s" dynamic=no] do={:if ([:typeof ($ours->[/ip firewall address-list get $e address])]!="nothing" && [:pick [:tostr [/ip firewall address-list get $e comment]] 0 6]!="mtvpn:") do={/ip firewall address-list remove $e}}`, n.List),
	)
	for _, x := range e {
		sub := "yes"
		if x.Exact {
			sub = "no"
		}
		out = append(out,
			fmt.Sprintf(`/ip dns static add name=%s type=FWD match-subdomain=%s forward-to=%s address-list=%s comment="%s"`, x.Name, sub, n.Forwarder, n.List, tag),
			fmt.Sprintf(`:do {/ip firewall address-list add list=%s address=%s comment="%s"} on-error={}`, n.List, x.Name, tag),
		)
	}
	return append(out, "}")
}

// RemovalBlock removes a tag's entries: its DNS entries in the address list
// and its static address-list entries.
func RemovalBlock(n Names, tag string) []string {
	return []string{
		fmt.Sprintf(`/ip dns static remove [find comment="%s" address-list="%s"]`, tag, n.List),
		fmt.Sprintf(`/ip firewall address-list remove [find list="%s" comment="%s" dynamic=no]`, n.List, tag),
	}
}

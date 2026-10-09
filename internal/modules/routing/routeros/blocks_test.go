package routeros

import (
	"slices"
	"testing"
)

func TestServiceBlockMatchesTheDoc(t *testing.T) {
	got := ServiceBlock(Defaults, "openai", []Entry{{Name: "api.openai.com", Exact: true}, {Name: "openai.com"}}, false)
	want := []string{
		`{`,
		`/ip dns static remove [find comment="openai" address-list="to_vpn_list"]`,
		`/ip firewall address-list remove [find list="to_vpn_list" comment="openai" dynamic=no]`,
		`:local ours [:toarray ""]`,
		`:set ($ours->"openai.com") 1`,
		`:set ($ours->"api.openai.com") 1`,
		`:foreach e in=[/ip dns static find where address-list="to_vpn_list" type=FWD] do={:if ([:typeof ($ours->[/ip dns static get $e name])]!="nothing") do={/ip dns static remove $e}}`,
		`:foreach e in=[/ip firewall address-list find where list="to_vpn_list" dynamic=no] do={:if ([:typeof ($ours->[/ip firewall address-list get $e address])]!="nothing" && [:pick [:tostr [/ip firewall address-list get $e comment]] 0 6]!="mtvpn:") do={/ip firewall address-list remove $e}}`,
		`/ip dns static add name=openai.com type=FWD match-subdomain=yes forward-to=vpn-doh address-list=to_vpn_list comment="openai"`,
		`:do {/ip firewall address-list add list=to_vpn_list address=openai.com comment="openai"} on-error={}`,
		`/ip dns static add name=api.openai.com type=FWD match-subdomain=no forward-to=vpn-doh address-list=to_vpn_list comment="openai"`,
		`:do {/ip firewall address-list add list=to_vpn_list address=api.openai.com comment="openai"} on-error={}`,
		`}`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("service block:\n%s", join(got))
	}
	// a continued part has no tag-removal lines
	cont := ServiceBlock(Defaults, "openai", []Entry{{Name: "openai.com"}}, true)
	if slices.Contains(cont, want[1]) || slices.Contains(cont, want[2]) || cont[1] != `:local ours [:toarray ""]` {
		t.Fatalf("continued:\n%s", join(cont))
	}
	rm := RemovalBlock(Names{List: "vpn", Forwarder: "doh"}, "mine")
	if !slices.Equal(rm, []string{
		`/ip dns static remove [find comment="mine" address-list="vpn"]`,
		`/ip firewall address-list remove [find list="vpn" comment="mine" dynamic=no]`,
	}) {
		t.Fatalf("removal: %v", rm)
	}
}

func join(l []string) string {
	s := ""
	for _, x := range l {
		s += x + "\n"
	}
	return s
}

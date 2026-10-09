package routeros

import (
	"slices"
	"testing"
)

func TestParseReadOutput(t *testing.T) {
	dns, err := ParseDNS([]byte("openai|openai.com|true|FWD|vpn-doh\r\n" +
		"|chatgpt.com|false|FWD|vpn-doh\r\n" +
		"\r\n" +
		"set by|hand|x.org|true|FWD|8.8.8.8\r\n" +
		"mine|exact.example|false|A|\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []DNSEntry{
		{Comment: "openai", Name: "openai.com", Type: "FWD", ForwardTo: "vpn-doh", Subdomain: true},
		{Comment: "", Name: "chatgpt.com", Type: "FWD", ForwardTo: "vpn-doh"},
		{Comment: "set by|hand", Name: "x.org", Type: "FWD", ForwardTo: "8.8.8.8", Subdomain: true},
		{Comment: "mine", Name: "exact.example", Type: "A"},
	}
	if !slices.Equal(dns, want) {
		t.Fatalf("dns: %+v", dns)
	}
	list, err := ParseList([]byte("openai|openai.com\r\n|chatgpt.com\r\nmtvpn:doh|dns.google\r\na|b|c.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(list, []ListEntry{{"openai", "openai.com"}, {"", "chatgpt.com"}, {"mtvpn:doh", "dns.google"}, {"a|b", "c.com"}}) {
		t.Fatalf("list: %+v", list)
	}
	if _, err := ParseDNS([]byte("a|b|c\n")); err == nil {
		t.Fatal("a short DNS line must fail")
	}
	if _, err := ParseDNS([]byte("a|b.com|maybe|FWD|x\n")); err == nil {
		t.Fatal("a bad match-subdomain must fail")
	}
	if _, err := ParseList([]byte("no bar\n")); err == nil {
		t.Fatal("a list line without | must fail")
	}
	c, err := ParseCheck([]byte("** WARNING: connection is not using a post-quantum key exchange algorithm.\r\nversion|7.24.5 (stable)\r\nboard|RB5009UG+S+\r\nidentity|Home\r\nforwarder|1\r\npin|1\r\nentries|1264\r\n"))
	if err != nil || c != (Check{Version: "7.24.5 (stable)", Board: "RB5009UG+S+", Identity: "Home", Forwarder: 1, Pin: 1, Entries: 1264}) {
		t.Fatalf("check: %+v %v", c, err)
	}
	if _, err := ParseCheck([]byte("version|7\n")); err == nil {
		t.Fatal("a cut check must fail")
	}
}

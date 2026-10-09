package discovery_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
)

func TestNormaliseWebsite(t *testing.T) {
	for _, c := range []struct{ in, url, host, reg string }{
		{"https://www.example.com/some/page?x=1", "https://www.example.com/some/page?x=1", "www.example.com", "example.com"},
		{"  claude.ai ", "https://claude.ai/", "claude.ai", "claude.ai"},
		{"www.claude.ai", "https://www.claude.ai/", "www.claude.ai", "claude.ai"},
		{"me.github.io", "https://me.github.io/", "me.github.io", "me.github.io"},
		{"HTTP://Example.COM:8080/a#frag", "http://example.com:8080/a", "example.com", "example.com"},
		{"кинопоиск.рф", "https://xn--h1aaecngahu.xn--p1ai/", "xn--h1aaecngahu.xn--p1ai", "xn--h1aaecngahu.xn--p1ai"},
	} {
		got, err := discovery.Normalise(c.in)
		if err != nil || got.URL != c.url || got.Host != c.host || got.Registrable != c.reg {
			t.Errorf("Normalise(%q) = %+v, %v", c.in, got, err)
		}
	}
	for _, bad := range []string{"", "203.0.113.5", "https://[2001:db8::1]/", "localhost", "not a site", "ftp://example.com", "https://user@example.com/", "com"} {
		if got, err := discovery.Normalise(bad); err == nil {
			t.Errorf("Normalise(%q) = %+v, want refused", bad, got)
		}
	}
}

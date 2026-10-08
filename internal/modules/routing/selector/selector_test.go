package selector

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSelectors(t *testing.T) {
	for _, c := range []struct {
		in, want, tag string
		err           error
	}{
		{"anthropic", "v2fly:anthropic", "anthropic", nil},
		{"  V2FLY:Anthropic ", "v2fly:anthropic", "anthropic", nil},
		{"v2fly:geolocation-!cn", "v2fly:geolocation-!cn", "geolocation-!cn", nil},
		{"iplist:YouTube.com", "iplist:youtube.com", "youtube.com", nil},
		{"IPLIST:copilot", "iplist:copilot", "copilot", nil},
		{"iplist:beta:apple", "iplist:beta:apple", "apple", nil},
		{"iplist:alpha:apple", "", "", ErrUnknownPortal},
		{"foo:bar", "", "", ErrUnknownSource},
		{"ftp://x/y", "", "", ErrBadURL},
		{"https:///nohost", "", "", ErrBadURL},
		{"Mine=https://Example.com/List.txt", "mine=https://Example.com/List.txt", "mine", nil},
		{"https://example.com/a.txt?x=y", "https://example.com/a.txt?x=y", "a", nil},
		{"v2fly:bad name", "", "", ErrBadName},
		{"v2fly:", "", "", ErrBadName},
		{"", "", "", ErrEmpty},
	} {
		s, err := Parse(c.in)
		if !errors.Is(err, c.err) {
			t.Errorf("%q: %v, want %v", c.in, err, c.err)
			continue
		}
		if err != nil {
			continue
		}
		tag, terr := s.Tag()
		if s.String() != c.want || tag != c.tag || terr != nil {
			t.Errorf("%q → %q tag %q %v, want %q %q", c.in, s.String(), tag, terr, c.want, c.tag)
		}
	}
	s, _ := Parse("iplist:beta:apple")
	if s.Source != Iplist || s.Portal != "beta" || s.Name != "apple" {
		t.Errorf("pinned: %+v", s)
	}
	if _, err := Parse("foo:bar"); err == nil || !strings.Contains(err.Error(), "'foo'") {
		t.Errorf("the unknown source is not named: %v", err)
	}
}

func TestTags(t *testing.T) {
	for in, want := range map[string]string{
		"https://files.example.com/share/x/tunneled-domains.txt":                           "tunneled-domains",
		"mine=https://files.example.com/share/x/list.txt":                                  "mine",
		"https://iplist.opencck.org/?format=text&data=domains&wildcard=1&site=YouTube.com": "youtube.com",
		"https://iplist.opencck.org/?format=text&group=apple":                              "apple",
		"https://example.com/":         "example.com",
		"https://Example.com":          "example.com",
		"https://x/a.list":             "a",
		"https://x/dir/":               "dir",
		"https://x/a.b.conf":           "a.b",
		"https://x/list?token=abc=def": "list",
	} {
		s, err := Parse(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if s.Source != URL {
			t.Errorf("%s parsed as %s", in, s.Source)
		}
		if got, err := s.Tag(); got != want || err != nil {
			t.Errorf("%s → %q %v, want %q", in, got, err, want)
		}
	}
	s, _ := Parse("https://x/list?token=abc=def")
	if s.NamedTag != "" || s.String() != "https://x/list?token=abc=def" {
		t.Errorf("a URL with = in its query was split: %+v", s)
	}
	s, _ = Parse("https://x/.txt")
	if got, err := s.Tag(); got != ".txt" || !errors.Is(err, ErrBadTag) {
		t.Errorf(".txt: %q %v", got, err)
	}
}

func TestValidTag(t *testing.T) {
	for _, bad := range []string{"telegram-cidr", "", `a"b`, "a$b", "a b", strings.Repeat("a", 64), "mtvpn:x", "-a", "Abc"} {
		if err := ValidTag(bad); !errors.Is(err, ErrBadTag) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"geolocation-!cn", "a", "youtube.com", "my_sites", strings.Repeat("a", 63)} {
		if err := ValidTag(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestPinAndSlug(t *testing.T) {
	s, _ := Parse("iplist:x")
	if got := s.Pin("main").String(); got != "iplist:x" {
		t.Errorf("Pin(main): %s", got)
	}
	if got := s.Pin("beta").String(); got != "iplist:beta:x" {
		t.Errorf("Pin(beta): %s", got)
	}
	r, _ := Parse("iplist:russia:x")
	if got := r.Pin("beta").String(); got != "iplist:russia:x" {
		t.Errorf("a pinned selector moved: %s", got)
	}
	v, _ := Parse("v2fly:x")
	if got := v.Pin("beta").String(); got != "v2fly:x" {
		t.Errorf("v2fly pinned: %s", got)
	}
	for in, want := range map[string]string{
		"My sites": "my-sites", "Мои сайты": "", "  Work -- VPN!! ": "work-vpn", "Кино 2": "2",
		strings.Repeat("ab ", 40): strings.TrimRight(strings.Repeat("ab-", 21), "-"),
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

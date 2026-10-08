// Package domain is pure: the domain-name check, covering, IDN, the
// registrable domain, and normalising what the admin types (normalise.go).
package domain

import (
	"regexp"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

var name = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Valid reports whether d is a domain name RouterOS can use: lowercase ASCII
// labels with at least one dot, at most 253 characters.
func Valid(d string) bool { return len(d) <= 253 && name.MatchString(d) }

// Covers reports whether a suffix entry for suffix covers name.
func Covers(suffix, name string) bool {
	return name == suffix || strings.HasSuffix(name, "."+suffix)
}

// Parents lists the names above name that still have a dot, nearest first:
// "a.b.c.d" → "b.c.d", "c.d".
func Parents(name string) []string {
	var out []string
	for {
		i := strings.IndexByte(name, '.')
		if i < 0 {
			return out
		}
		name = name[i+1:]
		if !strings.Contains(name, ".") {
			return out
		}
		out = append(out, name)
	}
}

// Registrable is the name a registrant controls, by the public suffix list:
// www.claude.ai → claude.ai, me.github.io → me.github.io.
func Registrable(host string) (string, error) {
	return publicsuffix.EffectiveTLDPlusOne(strings.TrimSuffix(strings.ToLower(host), "."))
}

// Unicode is the Unicode form of a punycode name, "" when d has no xn-- label.
func Unicode(d string) string {
	if !strings.HasPrefix(d, "xn--") && !strings.Contains(d, ".xn--") {
		return ""
	}
	u, err := idna.Display.ToUnicode(d)
	if err != nil || u == d {
		return ""
	}
	return u
}

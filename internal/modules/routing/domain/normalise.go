package domain

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// What Normalise refuses.
var (
	ErrIP          = errors.New("domain: an IP address")
	ErrInvalid     = errors.New("domain: not a valid domain name")
	ErrUnsupported = errors.New("domain: not a domain rule")
	ErrEmpty       = errors.New("domain: empty")
)

// Name is one normalised domain.
type Name struct {
	Domain  string // punycode, lowercase
	Exact   bool
	Unicode string // the Unicode form when Domain is an IDN; "" otherwise
}

// Normalise turns what the admin typed or pasted into a name: v2fly prefixes,
// URLs, "*." and host:port forms are understood; IPs and rules RouterOS can't
// express are refused. Plain names are suffix.
func Normalise(s string) (Name, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '#'); i >= 0 && !strings.Contains(s[:i], "://") {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.ToLower(s)
	if s == "" {
		return Name{}, ErrEmpty
	}
	var n Name
	for _, p := range []string{"regexp:", "keyword:", "include:"} {
		if strings.HasPrefix(s, p) {
			return Name{}, ErrUnsupported
		}
	}
	switch {
	case strings.HasPrefix(s, "domain:"):
		s = strings.TrimSpace(strings.TrimPrefix(s, "domain:"))
	case strings.HasPrefix(s, "full:"):
		s, n.Exact = strings.TrimSpace(strings.TrimPrefix(s, "full:")), true
	}
	host, err := hostOf(s)
	if err != nil {
		return Name{}, err
	}
	if strings.HasPrefix(host, "*.") {
		host, n.Exact = host[2:], false
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return Name{}, ErrEmpty
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return Name{}, ErrIP
	}
	n.Domain = host
	if !isASCII(host) {
		a, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return Name{}, ErrInvalid
		}
		n.Domain, n.Unicode = a, host
	}
	if !Valid(n.Domain) {
		return Name{}, ErrInvalid
	}
	return n, nil
}

// hostOf strips a scheme, userinfo, path, query, fragment and port.
func hostOf(s string) (string, error) {
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", ErrInvalid
		}
		s = u.Host
	} else if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	if strings.HasPrefix(s, "[") {
		if i := strings.IndexByte(s, ']'); i > 0 {
			if _, err := netip.ParseAddr(s[1:i]); err == nil {
				return "", ErrIP
			}
		}
		return "", ErrInvalid
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", ErrIP
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && digits(s[i+1:]) {
		s = s[:i]
	}
	return s, nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

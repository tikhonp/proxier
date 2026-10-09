package discovery

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"golang.org/x/net/idna"
)

// ErrWebsite is input that isn't a website's name.
var ErrWebsite = errors.New("discovery: not a website")

// Target is what a run visits.
type Target struct {
	URL, Host, Registrable string
}

// Normalise reads a URL or a domain: no scheme means https; the host is
// lowercased and turned into punycode; an IP or a host that isn't a domain
// name is refused; the registrable domain is the public suffix list's. The
// URL visited keeps the path and query.
func Normalise(input string) (Target, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return Target{}, ErrWebsite
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return Target{}, ErrWebsite
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if _, err := netip.ParseAddr(host); err == nil {
		return Target{}, ErrWebsite
	}
	if host, err = idna.Lookup.ToASCII(host); err != nil || !domain.Valid(host) {
		return Target{}, ErrWebsite
	}
	reg, err := domain.Registrable(host)
	if err != nil {
		return Target{}, ErrWebsite
	}
	u.Scheme = strings.ToLower(u.Scheme)
	port := u.Port()
	u.Host = host
	if port != "" {
		u.Host = host + ":" + port
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	return Target{URL: u.String(), Host: host, Registrable: reg}, nil
}

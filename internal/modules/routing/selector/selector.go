// Package selector is pure: the selector grammar of domain sources
// (docs/integrations/domain-sources.md#selector-grammar), tags and portals.
package selector

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// What Parse and Tag refuse.
var (
	ErrEmpty         = errors.New("selector: empty")
	ErrBadURL        = errors.New("selector: only http:// and https:// addresses with a host")
	ErrBadName       = errors.New("selector: not a valid list, site or group name")
	ErrUnknownPortal = errors.New("selector: unknown iplist portal")
	ErrUnknownSource = errors.New("selector: unknown source")
	ErrBadTag        = errors.New("selector: can't make a tag")
)

// Source is where a service's names come from.
type Source string

const (
	V2fly  Source = "v2fly"
	Iplist Source = "iplist"
	URL    Source = "url"
	Custom Source = "custom"
)

// Portals are iplist's portals in resolution order.
var Portals = []string{"main", "beta", "russia"}

// Selector is a parsed selector.
type Selector struct {
	Source   Source
	Name     string // v2fly list; iplist site or group
	Portal   string // iplist: "" or a portal
	URL      string // as typed
	NamedTag string // url: the "<tag>=" prefix, lowercased
}

var (
	namedURL  = regexp.MustCompile(`(?i)^([a-z0-9][a-z0-9._-]*)=([a-z][a-z0-9+.-]*://.+)$`)
	v2flyName = regexp.MustCompile(`^[a-z0-9][a-z0-9._!-]{0,62}$`)
	siteName  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,252}$`)
)

// Parse reads a selector as the admin typed it. A bare name means v2fly.
func Parse(s string) (Selector, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Selector{}, ErrEmpty
	}
	if m := namedURL.FindStringSubmatch(s); m != nil {
		if err := checkURL(m[2]); err != nil {
			return Selector{}, err
		}
		return Selector{Source: URL, URL: m[2], NamedTag: strings.ToLower(m[1])}, nil
	}
	if strings.Contains(s, "://") {
		if err := checkURL(s); err != nil {
			return Selector{}, err
		}
		return Selector{Source: URL, URL: s}, nil
	}
	prefix, rest, found := strings.Cut(s, ":")
	if !found {
		return named(V2fly, "", strings.ToLower(s))
	}
	rest = strings.ToLower(rest)
	switch strings.ToLower(prefix) {
	case "v2fly":
		return named(V2fly, "", rest)
	case "iplist":
		portal, sel, pinned := strings.Cut(rest, ":")
		if !pinned {
			return named(Iplist, "", rest)
		}
		if !slices.Contains(Portals, portal) {
			return Selector{}, fmt.Errorf("%w '%s'", ErrUnknownPortal, portal)
		}
		return named(Iplist, portal, sel)
	}
	return Selector{}, fmt.Errorf("%w '%s'", ErrUnknownSource, strings.ToLower(prefix))
}

func named(src Source, portal, name string) (Selector, error) {
	re := v2flyName
	if src == Iplist {
		re = siteName
	}
	if !re.MatchString(name) {
		return Selector{}, ErrBadName
	}
	return Selector{Source: src, Name: name, Portal: portal}, nil
}

func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ErrBadURL
	}
	return nil
}

// String is the stored spelling: v2fly:anthropic, iplist:beta:apple, the URL
// as typed, mine=<url>.
func (s Selector) String() string {
	switch s.Source {
	case V2fly:
		return "v2fly:" + s.Name
	case Iplist:
		if s.Portal != "" {
			return "iplist:" + s.Portal + ":" + s.Name
		}
		return "iplist:" + s.Name
	case URL:
		if s.NamedTag != "" {
			return s.NamedTag + "=" + s.URL
		}
		return s.URL
	}
	return ""
}

// Pin pins an unpinned iplist selector to portal, unless portal is the first
// one: a selector found on main stays as typed.
func (s Selector) Pin(portal string) Selector {
	if s.Source == Iplist && s.Portal == "" && portal != Portals[0] {
		s.Portal = portal
	}
	return s
}

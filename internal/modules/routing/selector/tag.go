package selector

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._!-]{0,62}$`)

// reservedTag is the comment of the router script's own Telegram block.
const reservedTag = "telegram-cidr"

// ValidTag checks a tag: what RouterOS comments and Shadowrocket "# tag" lines
// can carry, never empty (untagged router entries are), never reserved.
func ValidTag(t string) error {
	if !tagRe.MatchString(t) || t == reservedTag {
		return fmt.Errorf("%w: '%s'", ErrBadTag, t)
	}
	return nil
}

var extensions = []string{".txt", ".list", ".lst", ".dat", ".conf", ".md"}

// Tag computes the selector's tag without the network.
func (s Selector) Tag() (string, error) {
	var t string
	switch s.Source {
	case V2fly, Iplist:
		t = s.Name
	case URL:
		t = s.NamedTag
		if t == "" {
			t = urlTag(s.URL)
		}
	default:
		return "", ErrBadTag
	}
	if err := ValidTag(t); err != nil {
		return t, err
	}
	return t, nil
}

func urlTag(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, k := range []string{"site", "group"} {
		if v := q.Get(k); v != "" {
			return strings.ToLower(v)
		}
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	if p == "" {
		return strings.ToLower(u.Hostname())
	}
	seg := strings.ToLower(p[strings.LastIndexByte(p, '/')+1:])
	for _, ext := range extensions {
		if base, ok := strings.CutSuffix(seg, ext); ok && base != "" {
			return base
		}
	}
	return seg
}

// Slug makes a tag from a custom service's name: ASCII letters and digits,
// every other run a "-".
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	s := b.String()
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

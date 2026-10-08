package sources

import (
	"strings"

	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

// Entry is one name of a list, with its attributes ("ads", "cn").
type Entry struct {
	Name  string
	Exact bool
	Attrs []string
}

// Include is an include: line with its attribute filter.
type Include struct {
	Name    string
	With    []string // @a: only entries that carry every one
	Without []string // @-a: no entry that carries any
}

// List is one parsed list file.
type List struct {
	Entries  []Entry
	Includes []Include
	Skipped  []snapshot.Skipped
}

// The reasons of skipped entries (i18n keys).
const (
	SkipUnsupported = "services.skip.unsupported"
	SkipInvalid     = "services.skip.invalid"
)

// ParseList reads a list in v2fly's format: "#" starts a comment anywhere;
// the first field is the rule, "@attr" fields are attributes, "&list"
// affiliations are ignored. regexp: and keyword: can't be expressed on
// RouterOS and are skipped, as are invalid names.
func ParseList(text string) List {
	var l List
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		rule := fields[0]
		var attrs []string
		for _, f := range fields[1:] {
			if a, ok := strings.CutPrefix(f, "@"); ok && a != "" {
				attrs = append(attrs, strings.ToLower(a))
			}
		}
		kind, value, found := strings.Cut(rule, ":")
		if !found {
			kind, value = "domain", rule
		}
		switch strings.ToLower(kind) {
		case "include":
			inc := Include{Name: strings.ToLower(value)}
			for _, a := range attrs {
				if neg, ok := strings.CutPrefix(a, "-"); ok {
					inc.Without = append(inc.Without, neg)
				} else {
					inc.With = append(inc.With, a)
				}
			}
			l.Includes = append(l.Includes, inc)
		case "regexp", "keyword":
			l.Skipped = append(l.Skipped, snapshot.Skipped{Entry: rule, Reason: SkipUnsupported})
		case "domain", "full":
			name := strings.ToLower(value)
			if !domain.Valid(name) {
				l.Skipped = append(l.Skipped, snapshot.Skipped{Entry: rule, Reason: SkipInvalid})
				continue
			}
			l.Entries = append(l.Entries, Entry{Name: name, Exact: strings.EqualFold(kind, "full"), Attrs: attrs})
		default:
			l.Skipped = append(l.Skipped, snapshot.Skipped{Entry: rule, Reason: SkipInvalid})
		}
	}
	return l
}

// set makes the snapshot of resolved entries.
func set(entries []Entry, skipped []snapshot.Skipped) snapshot.Set {
	var suffix, exact []string
	for _, e := range entries {
		if e.Exact {
			exact = append(exact, e.Name)
		} else {
			suffix = append(suffix, e.Name)
		}
	}
	return snapshot.New(suffix, exact, skipped)
}

package routeros

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Op is a block ParseScript read back.
type Op struct {
	Kind    string // update, continue, remove
	Tag     string
	Names   Names
	Entries []Entry
}

var (
	reRemoveDNS = regexp.MustCompile(`^/ip dns static remove \[find comment="([^"]*)" address-list="([A-Za-z0-9._-]+)"\]$`)
	reAddDNS    = regexp.MustCompile(`^/ip dns static add name=(\S+) type=FWD match-subdomain=(yes|no) forward-to=([A-Za-z0-9._-]+) address-list=([A-Za-z0-9._-]+) comment="([^"]*)"$`)
)

// ParseScript reads back what ServiceBlock and RemovalBlock wrote; comment
// and blank lines are skipped. Anything else is an error: every block read is
// built again and must equal its lines.
func ParseScript(body []byte) ([]Op, error) {
	var src []string
	for _, l := range strings.Split(string(body), "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			continue
		}
		src = append(src, l)
	}
	var ops []Op
	for i := 0; i < len(src); {
		l := src[i]
		switch {
		case l == "{":
			end := slices.Index(src[i:], "}")
			if end < 0 {
				return nil, fmt.Errorf("routeros: block at line %d never ends", i+1)
			}
			op, err := parseBlock(src[i : i+end+1])
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i += end + 1
		case reRemoveDNS.MatchString(l):
			m := reRemoveDNS.FindStringSubmatch(l)
			n := Names{List: m[2]}
			want := RemovalBlock(n, m[1])
			if i+1 >= len(src) || src[i+1] != want[1] {
				return nil, fmt.Errorf("routeros: removal of %s without its address-list line", m[1])
			}
			ops = append(ops, Op{Kind: "remove", Tag: m[1], Names: n})
			i += 2
		default:
			return nil, fmt.Errorf("routeros: unexpected line %q", l)
		}
	}
	return ops, nil
}

func parseBlock(lines []string) (Op, error) {
	op := Op{Kind: "update"}
	if len(lines) > 1 && reRemoveDNS.MatchString(lines[1]) {
		op.Tag = reRemoveDNS.FindStringSubmatch(lines[1])[1]
	} else {
		op.Kind = "continue"
	}
	for _, l := range lines {
		m := reAddDNS.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		op.Entries = append(op.Entries, Entry{Name: m[1], Exact: m[2] == "no"})
		op.Names = Names{List: m[4], Forwarder: m[3]}
		if op.Tag == "" {
			op.Tag = m[5]
		}
	}
	if op.Tag == "" || len(op.Entries) == 0 {
		return Op{}, fmt.Errorf("routeros: a block without names")
	}
	if want := ServiceBlock(op.Names, op.Tag, op.Entries, op.Kind == "continue"); !slices.Equal(want, lines) {
		return Op{}, fmt.Errorf("routeros: block of %s isn't one Proxier writes", op.Tag)
	}
	return op, nil
}

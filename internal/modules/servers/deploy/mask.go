package deploy

import (
	"sort"
	"strings"
)

// Secrets are a server's secret values by key: its generated values and secret
// parameters. A key can hold several values, because an old deployment's files
// were rendered with values that a rotation or a parameter change replaced.
type Secrets map[string][]string

// Add records value under key; an empty value hides nothing and is ignored.
func (s Secrets) Add(key, value string) {
	if value == "" {
		return
	}
	for _, v := range s[key] {
		if v == value {
			return
		}
	}
	s[key] = append(s[key], value)
}

// AddAll records every value of m.
func (s Secrets) AddAll(m map[string]string) {
	for k, v := range m {
		s.Add(k, v)
	}
}

func (s Secrets) has(key, value string) bool {
	for _, v := range s[key] {
		if v == value {
			return true
		}
	}
	return false
}

// Masker replaces secret values in file contents by placeholders, longest
// value first so one secret that holds another is hidden whole.
type Masker struct {
	r *strings.Replacer
}

// NewMasker builds a masker: each value becomes •••key•••. other is the set on
// the other side of a comparison; where it holds the same key without this
// value, the placeholder says which side this is (side is "old" or "new"), so
// a diff of a changed secret still shows a change.
func NewMasker(own, other Secrets, side string) Masker {
	type pair struct{ value, ph string }
	var pairs []pair
	for k, vals := range own {
		for _, v := range vals {
			ph := "•••" + k + "•••"
			if _, ok := other[k]; ok && !other.has(k, v) && side != "" {
				ph = "•••" + k + " (" + side + ")•••"
			}
			pairs = append(pairs, pair{v, ph})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].value) != len(pairs[j].value) {
			return len(pairs[i].value) > len(pairs[j].value)
		}
		return pairs[i].value < pairs[j].value
	})
	args := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		args = append(args, p.value, p.ph)
	}
	return Masker{r: strings.NewReplacer(args...)}
}

// Apply returns b with every secret replaced.
func (m Masker) Apply(b []byte) []byte {
	if m.r == nil {
		return b
	}
	return []byte(m.r.Replace(string(b)))
}

// Mask is the masker of one side with nothing to compare against.
func Mask(s Secrets) Masker { return NewMasker(s, nil, "") }

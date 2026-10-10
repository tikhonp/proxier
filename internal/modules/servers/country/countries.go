// Package country knows the ISO 3166-1 countries a server can be in: the flag
// emoji and the names in both UI languages. A server stores only the code;
// everything else is derived here (ADR 0015).
package country

import (
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

var (
	en = display.Regions(language.English)
	ru = display.Regions(language.Russian)
)

// Valid reports whether code is an upper-case ISO 3166-1 alpha-2 country in
// use today: a withdrawn code (UK, DD, SU…) is not, since the picker would
// show it beside the country that replaced it under the same name.
func Valid(code string) bool {
	if len(code) != 2 || code != strings.ToUpper(code) {
		return false
	}
	r, err := language.ParseRegion(code)
	return err == nil && r.IsCountry() && r.String() == code && r.Canonicalize() == r && en.Name(r) != ""
}

// Flag is the flag emoji of a valid code ("NL" → 🇳🇱), "" for an invalid one.
func Flag(code string) string {
	if !Valid(code) {
		return ""
	}
	const base = 0x1F1E6 - 'A'
	return string([]rune{base + rune(code[0]), base + rune(code[1])})
}

// Name is the country's name in lang ("ru" or anything else for English).
func Name(code, lang string) string {
	if !Valid(code) {
		return code
	}
	r := language.MustParseRegion(code)
	if lang == "ru" {
		return ru.Name(r)
	}
	return en.Name(r)
}

// Entry is a country for a picker.
type Entry struct{ Code, Flag, Name string }

var (
	listOnce sync.Once
	all      []string
)

// Codes lists every valid country code, sorted.
func Codes() []string {
	listOnce.Do(func() {
		for a := 'A'; a <= 'Z'; a++ {
			for b := 'A'; b <= 'Z'; b++ {
				if c := string([]rune{a, b}); Valid(c) {
					all = append(all, c)
				}
			}
		}
	})
	return all
}

// List is every country with its name in lang, sorted by that name.
func List(lang string) []Entry {
	codes := Codes()
	out := make([]Entry, len(codes))
	for i, c := range codes {
		out[i] = Entry{Code: c, Flag: Flag(c), Name: Name(c, lang)}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Label is how the picker shows a country, and what it puts in the field:
// "🇳🇱 Netherlands · NL". Parse reads it back.
func Label(code, lang string) string {
	if !Valid(code) {
		return code
	}
	return Flag(code) + " " + Name(code, lang) + " · " + code
}

// Prefix is the first half of the name of a server created in the country:
// its code in lower case ("NL" → "nl").
func Prefix(code string) string { return strings.ToLower(code) }

// Parse reads what was typed in the country picker: a Label in either
// language, a code in any case, or a name in English or Russian in any case.
// It returns the code, or "" when the text names no country.
func Parse(s string) string {
	s = strings.TrimSpace(s)
	if _, code, ok := cutLast(s, "·"); ok {
		s = code
	}
	if c := strings.ToUpper(s); Valid(c) {
		return c
	}
	s = strings.TrimSpace(strings.TrimLeftFunc(s, isFlagRune))
	for _, c := range Codes() {
		if strings.EqualFold(s, Name(c, "en")) || strings.EqualFold(s, Name(c, "ru")) {
			return c
		}
	}
	return ""
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):]), true
}

// isFlagRune is a regional indicator, the two letters a flag emoji is made of.
func isFlagRune(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

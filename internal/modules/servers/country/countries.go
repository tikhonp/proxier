// Package country knows the ISO 3166-1 countries a location can be in: the
// flag emoji and the names in both UI languages.
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

// Valid reports whether code is an upper-case ISO 3166-1 alpha-2 country.
func Valid(code string) bool {
	if len(code) != 2 || code != strings.ToUpper(code) {
		return false
	}
	r, err := language.ParseRegion(code)
	return err == nil && r.IsCountry() && r.String() == code
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

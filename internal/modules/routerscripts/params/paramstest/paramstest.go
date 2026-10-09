// Package paramstest holds today's real fresh-router.rsc (byte for byte from
// ~/projects/mikrotik at b1e34f2) and builds its variants in code. Nobody
// edits the file: a later change of the real script is copied in as a new
// fixture. Only tests import this package.
package paramstest

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
)

//go:embed fresh-router.rsc
var today []byte

// Today is the embedded fresh-router.rsc.
func Today() []byte { return bytes.Clone(today) }

// WithEnd inserts "# END PARAMETERS" on the line after :local dohForwarder.
func WithEnd(b []byte) []byte {
	loc := regexp.MustCompile(`(?m)^:local dohForwarder .*\n`).FindIndex(b)
	if loc == nil {
		panic("paramstest: no :local dohForwarder line")
	}
	return join(b[:loc[1]], []byte("# END PARAMETERS\n"), b[loc[1]:])
}

// Annotate inserts a line "# <comment>" directly above :local <name>.
func Annotate(b []byte, name, comment string) []byte {
	loc := regexp.MustCompile(`(?m)^:local ` + regexp.QuoteMeta(name) + `[ \t]`).FindIndex(b)
	if loc == nil {
		panic("paramstest: no :local " + name + " line")
	}
	return join(b[:loc[0]], []byte("# "+comment+"\n"), b[loc[0]:])
}

// Replace replaces old with new; old must occur exactly once.
func Replace(b []byte, old, new string) []byte {
	if n := bytes.Count(b, []byte(old)); n != 1 {
		panic(fmt.Sprintf("paramstest: %q occurs %d times", old, n))
	}
	return bytes.Replace(b, []byte(old), []byte(new), 1)
}

func join(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

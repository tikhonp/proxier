package params_test

import (
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
)

func TestLiterals(t *testing.T) {
	quoted := params.Param{Name: "a", Literal: `"x"`, Default: "x"}
	for in, want := range map[string]string{
		`a"b$c`:      `"a\"b\$c"`,
		`C:\dir`:     `"C:\\dir"`,
		`what?`:      `"what\?"`,
		``:           `""`,
		`10.40.1`:    `"10.40.1"`,
		`привет мир`: `"привет мир"`,
	} {
		got, err := params.Literal(quoted, in)
		if err != nil || got != want {
			t.Errorf("Literal(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"a\nb", "a\tb", "a\x7fb", "a\rb", string([]byte{0xff, 0xfe})} {
		if _, err := params.Literal(quoted, bad); !errors.Is(err, params.ErrControl) {
			t.Errorf("Literal(%q): %v", bad, err)
		}
	}
	bare := params.Param{Name: "n", Literal: "3", Default: "3", Bare: true}
	if got, err := params.Literal(bare, "8"); err != nil || got != "8" {
		t.Errorf("bare 8: %q %v", got, err)
	}
	for _, bad := range []string{"two words", "", `"8"`, "$x"} {
		if _, err := params.Literal(bare, bad); !errors.Is(err, params.ErrBare) {
			t.Errorf("bare %q: %v", bad, err)
		}
	}
	// quoted defaults with escapes read back
	s := params.Parse(block(`:local a "say \"hi\""`, `:local b "x\_y"`, `:local c "\41\42"`, `:local d "\\\$\?"`, `:local e "\a\n"`))
	if len(s.Findings) != 0 {
		t.Fatalf("findings: %s", keys(s.Findings))
	}
	for name, want := range map[string]string{"a": `say "hi"`, "b": "x y", "c": "AB", "d": `\$?`, "e": "\a\n"} {
		if got := mustParam(t, s, name).Default; got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	// what Literal writes reads back as the same value
	p := mustParam(t, s, "a")
	for _, v := range []string{`a"b$c`, `\`, "?", "", "x y"} {
		lit, _ := params.Literal(p, v)
		back := params.Parse(block(":local a " + lit))
		if got := mustParam(t, back, "a").Default; got != v {
			t.Errorf("round trip %q: %q", v, got)
		}
	}
}

func TestCheckValues(t *testing.T) {
	s := params.Parse(block("# @required", ":local req \"\"", "# @choices US|RU", ":local cc \"RU\"", "# @pattern [0-9]+\\.[0-9]+", ":local ver \"1.2\"",
		":local free \"x\"", ":local n 3"))
	for _, c := range []struct {
		name, value, want string
	}{
		{"req", "", "params.err.required"},
		{"req", "  ", "params.err.required"},
		{"req", "x", ""},
		{"cc", "DE", "params.err.choice"},
		{"cc", " US ", ""},
		{"cc", "", ""},
		{"ver", "1.2.3", "params.err.pattern"},
		{"ver", "x1.2", "params.err.pattern"},
		{"ver", "10.20", ""},
		{"free", "a\tb", "params.err.control"},
		{"free", "", ""},
		{"n", "two words", "params.err.bare"},
		{"n", "8", ""},
	} {
		if got := params.Check(mustParam(t, s, c.name), c.value); got != c.want {
			t.Errorf("Check(%s, %q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
}

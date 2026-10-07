package render_test

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

func exec(t *testing.T, src string, data any) (string, error) {
	t.Helper()
	tpl, err := template.New("t").Funcs(render.Funcs()).Option("missingkey=error").Parse(src)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = tpl.Execute(&b, data)
	return b.String(), err
}

func TestFunctionMap(t *testing.T) {
	data := map[string]any{"s": "a<b>&\"c\"", "list": []string{"x", "y", "z"}, "empty": "", "n": 0, "pad": "l1\nl2"}
	cases := []struct{ src, want string }{
		{`{{ json .s }}`, `"a<b>&\"c\""`}, // no HTML escaping
		{`{{ json .list }}`, `["x","y","z"]`},
		{`{{ quote "a b" }}`, `"a b"`},
		{`{{ .empty | default "fallback" }}`, `fallback`},
		{`{{ .s | default "fallback" }}`, `a<b>&"c"`},
		{`{{ .n | default 7 }}`, `7`},
		{`{{ lower "AbC" }}{{ upper "AbC" }}`, `abcABC`},
		{`{{ trim "  x  " }}`, `x`},
		{`{{ urlquery "a b/c" }}`, `a+b%2Fc`},
		{`{{ b64enc "hello" }}`, `aGVsbG8=`},
		{`{{ sha256 "abc" }}`, `ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad`},
		{`{{ .list | join "," }}`, `x,y,z`},
		{`{{ .pad | indent 2 }}`, "  l1\n  l2"},
		// builtins stay
		{`{{ if eq (len .list) 3 }}three{{ end }}`, `three`},
	}
	for _, c := range cases {
		got, err := exec(t, c.src, data)
		if err != nil || got != c.want {
			t.Errorf("%s = %q (%v), want %q", c.src, got, err, c.want)
		}
	}
	// nothing that reaches the host
	for _, fn := range []string{`env "HOME"`, `readFile "/etc/passwd"`, `exec "id"`, `getenv "HOME"`, `now`, `include "x"`} {
		if _, err := exec(t, `{{ `+fn+` }}`, nil); err == nil || !strings.Contains(err.Error(), "not defined") {
			t.Errorf("%s must not be a function: %v", fn, err)
		}
	}
	if _, err := exec(t, `{{ join "," 5 }}`, nil); err == nil {
		t.Error("join of a non-list must fail")
	}
}

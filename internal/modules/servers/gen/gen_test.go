package gen_test

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/gen"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
)

func TestGeneratedValues(t *testing.T) {
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	v, err := gen.New(manifest.Generated{Key: "u", Kind: "uuid"})
	if err != nil || !uuidRe.MatchString(v) {
		t.Errorf("uuid %q %v", v, err)
	}
	if v, _ := gen.New(manifest.Generated{Key: "u", Kind: "uuid", Prefix: "id-"}); !strings.HasPrefix(v, "id-") || !uuidRe.MatchString(v[3:]) {
		t.Errorf("uuid with prefix: %q", v)
	}
	// hex: exactly length characters, odd lengths too, prefix not counted
	for _, n := range []int{4, 5, 16, 128} {
		v, err := gen.New(manifest.Generated{Key: "h", Kind: "hex", Length: n, Prefix: "/"})
		if err != nil || len(v) != n+1 || v[0] != '/' || !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(v[1:]) {
			t.Errorf("hex %d: %q %v", n, v, err)
		}
	}
	// base64: std encoding of that many bytes
	v, err = gen.New(manifest.Generated{Key: "b", Kind: "base64", Bytes: 32})
	if raw, derr := base64.StdEncoding.DecodeString(v); err != nil || derr != nil || len(raw) != 32 {
		t.Errorf("base64 %q %v %v", v, err, derr)
	}
	// password: letters and digits only, and both kinds show up in a long one
	v, err = gen.New(manifest.Generated{Key: "p", Kind: "password", Length: 128})
	if err != nil || !regexp.MustCompile(`^[A-Za-z0-9]{128}$`).MatchString(v) {
		t.Errorf("password %q %v", v, err)
	}
	if !regexp.MustCompile(`[0-9]`).MatchString(v) || !regexp.MustCompile(`[a-z]`).MatchString(v) || !regexp.MustCompile(`[A-Z]`).MatchString(v) {
		t.Errorf("128 characters should hold digits, lower and upper case: %q", v)
	}
	// two values differ
	a, _ := gen.New(manifest.Generated{Key: "h", Kind: "hex", Length: 32})
	b, _ := gen.New(manifest.Generated{Key: "h", Kind: "hex", Length: 32})
	if a == b {
		t.Error("two random values were equal")
	}
	// bounds and kinds
	for _, g := range []manifest.Generated{
		{Key: "h", Kind: "hex", Length: 3}, {Key: "h", Kind: "hex", Length: 129},
		{Key: "p", Kind: "password", Length: 7}, {Key: "b", Kind: "base64", Bytes: 7}, {Key: "b", Kind: "base64", Bytes: 97},
		{Key: "x", Kind: "nope"},
	} {
		if _, err := gen.New(g); err == nil {
			t.Errorf("%+v must be refused", g)
		}
	}

	// Ensure keeps what exists, creates only what is missing, and keeps
	// values no longer declared
	decl := []manifest.Generated{{Key: "a", Kind: "uuid"}, {Key: "b", Kind: "hex", Length: 8}}
	all, created, err := gen.Ensure(decl, map[string]string{"a": "kept", "old": "unused"})
	if err != nil || all["a"] != "kept" || all["old"] != "unused" || len(all["b"]) != 8 || len(created) != 1 || created[0] != "b" {
		t.Errorf("ensure: %v %v %v", all, created, err)
	}
	again, created, _ := gen.Ensure(decl, all)
	if len(created) != 0 || again["b"] != all["b"] {
		t.Errorf("a second ensure must change nothing: %v %v", again, created)
	}
	if _, _, err := gen.Ensure([]manifest.Generated{{Key: "x", Kind: "nope"}}, nil); err == nil {
		t.Error("an invalid declaration must fail")
	}
}

package output_test

import (
	"encoding/base64"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
)

func TestBase64Format(t *testing.T) {
	r := request(nl1(), de1())
	plain := build(t, r)
	r.Format = "uri-base64"
	b64 := build(t, r)
	if string(b64.Body) != base64.StdEncoding.EncodeToString(plain.Body) {
		t.Fatalf("base64 body %q", b64.Body)
	}
	f, _ := output.Lookup("uri-base64")
	if got := string(f.Render([]string{"a"})); got != "YQo=" { // "a\n", padded
		t.Errorf("%q", got)
	}
	if n := output.Names(); len(n) != 2 || n[0] != "uri-plain" || n[1] != "uri-base64" {
		t.Errorf("names %v", n)
	}
}

func TestPickFormat(t *testing.T) {
	both := []string{"uri-plain", "uri-base64"}
	plainOnly := []string{"uri-plain"}
	for _, c := range []struct {
		query    string
		allowed  []string
		def      string
		override string
		want     string
		err      bool
	}{
		{"uri-base64", both, "uri-plain", "", "uri-base64", false},
		{"uri-plain", both, "uri-base64", "uri-base64", "uri-plain", false}, // the query beats the override
		{"mihomo", both, "uri-plain", "", "", true},
		{"uri-base64", plainOnly, "uri-plain", "", "", true},
		{"", both, "uri-plain", "", "uri-plain", false},
		{"", both, "uri-plain", "uri-base64", "uri-base64", false},
		{"", plainOnly, "uri-plain", "uri-base64", "uri-plain", false}, // an override no longer allowed
	} {
		got, err := output.PickFormat(c.query, c.allowed, c.def, c.override)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
		if c.err && err != output.ErrBadFormat {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

package params_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
)

func TestFillChangesOnlyLiterals(t *testing.T) {
	body := paramstest.WithEnd(paramstest.Today())
	s := params.Parse(body)
	values := s.Values()
	values["lanNet"] = "10.40.1"
	out, err := params.Fill(body, values)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(body, []byte(":local lanNet \"10.230.1\"\n"), []byte(":local lanNet \"10.40.1\"\n"), 1)
	if !bytes.Equal(out, want) {
		t.Fatal("the output differs from the fixture elsewhere than lanNet's literal")
	}
	ol, bl := strings.Split(string(out), "\n"), strings.Split(string(body), "\n")
	if len(ol) != len(bl) {
		t.Fatalf("%d lines, want %d", len(ol), len(bl))
	}
	for i := range bl {
		if (ol[i] != bl[i]) != (i == 23) {
			t.Errorf("line %d: %q / %q", i+1, ol[i], bl[i])
		}
	}
	if params.Changed(s, values)[0] != "lanNet" || len(params.Changed(s, values)) != 1 {
		t.Errorf("changed: %v", params.Changed(s, values))
	}
	// all defaults → the same bytes, today's file too
	for _, b := range [][]byte{body, paramstest.Today()} {
		same, err := params.Fill(b, params.Parse(b).Values())
		if err != nil || !bytes.Equal(same, b) {
			t.Errorf("all defaults changed the file: %v", err)
		}
		if same, _ := params.Fill(b, nil); !bytes.Equal(same, b) {
			t.Error("no values changed the file")
		}
	}
	// escapes, a bare value, trailing spaces after the literal kept
	b := []byte("# PARAMETERS\r\n:local a \"x\"  \r\n:local n 3\r\n# END PARAMETERS\r\n:put $a\r\n")
	out, err = params.Fill(b, map[string]string{"a": `q"$`, "n": "8"})
	if err != nil || string(out) != "# PARAMETERS\r\n:local a \"q\\\"\\$\"  \r\n:local n 8\r\n# END PARAMETERS\r\n:put $a\r\n" {
		t.Errorf("CRLF: %q %v", out, err)
	}
	if _, err := params.Fill(body, map[string]string{"nope": "x"}); !errors.Is(err, params.ErrUnknown) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := params.Fill(body, map[string]string{"vpnGateway": "x"}); !errors.Is(err, params.ErrUnknown) {
		t.Errorf("computed: %v", err)
	}
	if _, err := params.Fill(b, map[string]string{"n": "two words"}); !errors.Is(err, params.ErrBare) {
		t.Errorf("bad value: %v", err)
	}
}

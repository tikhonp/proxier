package params_test

import (
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
)

func TestUnknownAnnotation(t *testing.T) {
	b := paramstest.Annotate(paramstest.WithEnd(paramstest.Today()), "subUrl", "@fil subscription-link")
	s := params.Parse(b)
	errs := s.Errors()
	if len(errs) != 1 || errs[0].Key != "params.err.annotation_unknown" || errs[0].Line != 28 {
		t.Fatalf("errors: %s", keys(errs))
	}
	if got := text(t, errs[0]); got != "Unknown annotation @fil." {
		t.Errorf("text: %q", got)
	}
	if p := mustParam(t, s, "subUrl"); p.Description != "mihomo subscription URL (SUB1 env of the container)" || p.Annotations.Fill != "" {
		t.Errorf("subUrl: %+v", p)
	}
}

func TestAnnotationRules(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string // keys of the findings
	}{
		{"secret", []string{"# @secret", ":local a \"x\""}, ""},
		{"required", []string{"# @required", ":local a \"\""}, ""},
		{"choices", []string{"# @choices US | RU|DE", ":local a \"RU\""}, ""},
		{"empty default with choices", []string{"# @choices a|b", ":local a \"\""}, ""},
		{"pattern", []string{"# @pattern [0-9]+(\\.[0-9]+){2}", ":local a \"10.1.2\""}, ""},
		{"every fill", []string{"# @fill subscription-link", ":local a \"\"", "# @fill routing-address-list", ":local b \"\"",
			"# @fill routing-doh-forwarder", ":local c \"\"", "# @fill proxier-ssh-key", ":local d \"\""}, ""},
		{"unknown", []string{"# @fil subscription-link", ":local a \"x\""}, "2:error:params.err.annotation_unknown"},
		{"text after secret", []string{"# @secret yes", ":local a \"x\""}, "2:error:params.err.annotation_args"},
		{"text after required", []string{"# @required always", ":local a \"x\""}, "2:error:params.err.annotation_args"},
		{"no choices", []string{"# @choices", ":local a \"x\""}, "2:error:params.err.choices"},
		{"an empty choice", []string{"# @choices a||b", ":local a \"a\""}, "2:error:params.err.choices"},
		{"a repeated choice", []string{"# @choices a|b|a", ":local a \"a\""}, "2:error:params.err.choices"},
		{"bad pattern", []string{"# @pattern [a-", ":local a \"x\""}, "2:error:params.err.pattern_bad"},
		{"empty pattern", []string{"# @pattern", ":local a \"x\""}, "2:error:params.err.pattern_bad"},
		{"fill without kind", []string{"# @fill", ":local a \"x\""}, "2:error:params.err.fill_kind"},
		{"fill unknown kind", []string{"# @fill wifi", ":local a \"x\""}, "2:error:params.err.fill_kind"},
		{"twice", []string{"# @secret", "# @secret", ":local a \"x\""}, "3:error:params.err.annotation_twice"},
		{"one fill on two", []string{"# @fill subscription-link", ":local a \"\"", "# @fill subscription-link", ":local b \"\""}, "4:error:params.err.fill_twice"},
		{"fill on bare", []string{"# @fill proxier-ssh-key", ":local a 1"}, "2:error:params.err.fill_bare"},
		{"fill with choices", []string{"# @choices a|b", "# @fill subscription-link", ":local a \"a\""}, "3:error:params.err.fill_choices"},
		{"default not a choice", []string{"# @choices a|b", ":local a \"c\""}, "3:error:params.err.default_choice"},
		{"above a computed value", []string{"# @secret", ":local a ($b . \"x\")"}, "2:warning:params.warn.annotation_computed"},
		{"followed by a blank line", []string{"# @secret", "", ":local a \"x\""}, "2:warning:params.warn.annotation_orphan"},
		{"followed by the end marker", []string{":local a \"x\"", "# @required"}, "3:warning:params.warn.annotation_orphan"},
		{"default off its pattern", []string{"# @pattern [0-9]+", ":local a \"x1\""}, "3:warning:params.warn.default_pattern"},
	}
	for _, c := range cases {
		s := params.Parse(block(c.lines...))
		if got := keys(s.Findings); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	s := params.Parse(block("# Wi-Fi password", "# @secret", "# @required", ":local pass \"x\"", "# @choices US|RU", ":local cc \"RU\"",
		"# @pattern [0-9]+", ":local n \"8\"", "# @fill proxier-ssh-key", ":local key \"\""))
	pass := mustParam(t, s, "pass")
	if pass.Description != "Wi-Fi password" || !pass.Annotations.Secret || !pass.Annotations.Required {
		t.Errorf("pass: %+v", pass)
	}
	if cc := mustParam(t, s, "cc"); !slices.Equal(cc.Annotations.Choices, []string{"US", "RU"}) {
		t.Errorf("cc: %+v", cc)
	}
	if n := mustParam(t, s, "n"); n.Annotations.Pattern != "[0-9]+" {
		t.Errorf("n: %+v", n)
	}
	if k := mustParam(t, s, "key"); k.Annotations.Fill != params.FillKey {
		t.Errorf("key: %+v", k)
	}
	// annotations outside the block are ordinary comments
	s = params.Parse([]byte("# @fil nonsense\n# PARAMETERS\n:local a \"1\"\n# END PARAMETERS\n# @secret\n:local b \"2\"\n"))
	if len(s.Findings) != 0 || len(s.Params) != 1 {
		t.Errorf("outside: %s", keys(s.Findings))
	}
}

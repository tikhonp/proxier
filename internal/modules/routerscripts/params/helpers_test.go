package params_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// names lists the parameters' names in order.
func names(s params.Script) []string {
	out := make([]string, 0, len(s.Params))
	for _, p := range s.Params {
		out = append(out, p.Name)
	}
	return out
}

// keys lists "line:severity:key" of the findings.
func keys(fs []params.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, itoa(f.Line)+":"+f.Severity+":"+f.Key)
	}
	return strings.Join(out, " ")
}

func itoa(n int) string { return strconv.Itoa(n) }

// block wraps lines in a PARAMETERS block with an end marker.
func block(lines ...string) []byte {
	return []byte("# PARAMETERS\n" + strings.Join(lines, "\n") + "\n# END PARAMETERS\n:put done\n")
}

func mustParam(t *testing.T, s params.Script, name string) params.Param {
	t.Helper()
	p, ok := s.Param(name)
	if !ok {
		t.Fatalf("no parameter %s in %v", name, names(s))
	}
	return p
}

// text renders a finding in English, as the pages do.
func text(t *testing.T, f params.Finding) string {
	t.Helper()
	c := i18n.NewCatalog()
	if err := c.Add("routerscripts", params.Messages()); err != nil {
		t.Fatal(err)
	}
	return c.Localizer(i18n.EN, nil).T(f.Key, i18n.Args(f.Args))
}

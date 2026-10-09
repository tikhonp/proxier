package params_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
)

func TestEvalConcatenation(t *testing.T) {
	s := params.Parse(paramstest.WithEnd(paramstest.Today()))
	if got := params.Eval(s, nil); got["vpnGateway"] != "192.168.89.2" || len(got) != 1 {
		t.Errorf("defaults: %v", got)
	}
	if got := params.Eval(s, map[string]string{"containerNet": "10.40.89"}); got["vpnGateway"] != "10.40.89.2" {
		t.Errorf("containerNet 10.40.89: %v", got)
	}
	// [command] and a $ver without a value give none
	today := params.Parse(paramstest.Today())
	got := params.Eval(today, nil)
	if _, ok := got["ver"]; ok {
		t.Errorf("ver: %v", got)
	}
	if _, ok := got["v"]; ok {
		t.Errorf("v: %v", got)
	}
	s = params.Parse(block(":local lanNet \"10.1.1\"", ":local same $lanNet", ":local quoted $\"lanNet\"", ":local gw ($lanNet . \".1\" . \"/24\")",
		":local later ($nope . \"x\")", ":local math ($n + 1)", ":local n 3", ":local early $after", ":local after \"z\""))
	got = params.Eval(s, map[string]string{"lanNet": "10.2.2"})
	for name, want := range map[string]string{"same": "10.2.2", "quoted": "10.2.2", "gw": "10.2.2.1/24"} {
		if got[name] != want {
			t.Errorf("%s: %q, want %q", name, got[name], want)
		}
	}
	for _, none := range []string{"later", "math", "early"} {
		if v, ok := got[none]; ok {
			t.Errorf("%s has a value %q", none, v)
		}
	}
}

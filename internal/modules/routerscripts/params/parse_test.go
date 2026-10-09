package params_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
)

var todaysNames = []string{"lanNet", "wanIface", "subUrl", "image", "timeZone", "lanIface", "containerIface", "vethName",
	"lanList", "wanList", "vpnList", "vpnTable", "vpnMark", "containerDisk", "containerNet", "dohHost", "dohIP", "dohForwarder"}

func TestTodaysScriptWithoutEndMarker(t *testing.T) {
	s := params.Parse(paramstest.Today())
	if !s.Block || s.Start != 22 || s.Ended || s.End != 66 {
		t.Fatalf("block %v start %d end %d ended %v", s.Block, s.Start, s.End, s.Ended)
	}
	if len(s.Findings) != 1 || s.Findings[0].Severity != params.Warning || s.Findings[0].Line != 66 {
		t.Fatalf("findings: %s", keys(s.Findings))
	}
	want := "The PARAMETERS block has no # END PARAMETERS line, so it ends at line 66; the last values read are minVer, ver, v."
	if got := text(t, s.Findings[0]); got != want {
		t.Errorf("warning: %q", got)
	}
	if got := names(s); len(got) != 19 || !slices.Equal(got[:18], todaysNames) || got[18] != "minVer" {
		t.Errorf("parameters: %v", got)
	}
	minVer := mustParam(t, s, "minVer")
	if !minVer.Bare || minVer.Default != "7024005" || minVer.Literal != "7024005" || minVer.Line != 64 {
		t.Errorf("minVer: %+v", minVer)
	}
	var computed []string
	for _, c := range s.Computed {
		computed = append(computed, c.Name+" = "+c.Expr)
	}
	if strings.Join(computed, "; ") != `vpnGateway = ($containerNet . ".2"); ver = [/system resource get version]; v = $ver` {
		t.Errorf("computed: %v", computed)
	}
	if len(s.Errors()) != 0 || len(s.Warnings()) != 1 {
		t.Errorf("errors %d warnings %d", len(s.Errors()), len(s.Warnings()))
	}
}

func TestTodaysScriptWithEndMarker(t *testing.T) {
	s := params.Parse(paramstest.WithEnd(paramstest.Today()))
	if len(s.Findings) != 0 {
		t.Fatalf("findings: %s", keys(s.Findings))
	}
	if !s.Ended || s.End != 58 {
		t.Errorf("end %d %v", s.End, s.Ended)
	}
	if got := names(s); !slices.Equal(got, todaysNames) {
		t.Errorf("parameters: %v", got)
	}
	if len(s.Computed) != 1 || s.Computed[0].Name != "vpnGateway" || s.Computed[0].Expr != `($containerNet . ".2")` || s.Computed[0].Line != 51 {
		t.Errorf("computed: %+v", s.Computed)
	}
	for name, want := range map[string]string{
		"lanNet":   "LAN /24 prefix (no trailing dot). Router gets .1, DHCP hands out .20-.254",
		"wanIface": "WAN port (gets its address, and the LAN's DNS servers, via DHCP client)",
		"subUrl":   "mihomo subscription URL (SUB1 env of the container)",
		"lanIface": "interface names",
		"dohHost":  "DoH resolver for tunneled services only. $dohForwarder must match DOH_FORWARDER in mtvpn.py.",
		"image":    "", "timeZone": "", "containerIface": "", "vethName": "",
	} {
		if got := mustParam(t, s, name).Description; got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
	lan := mustParam(t, s, "lanNet")
	if lan.Default != "10.230.1" || lan.Literal != `"10.230.1"` || lan.Bare || lan.Line != 24 {
		t.Errorf("lanNet: %+v", lan)
	}
	if v := s.Values(); len(v) != 18 || v["subUrl"] != "https://files....t" {
		t.Errorf("values: %v", v)
	}
}

func TestDescriptionIsTheRunDirectlyAbove(t *testing.T) {
	s := params.Parse(paramstest.WithEnd(paramstest.Today()))
	if d := mustParam(t, s, "lanIface").Description; d != "interface names" {
		t.Errorf("lanIface: %q", d)
	}
	if d := mustParam(t, s, "containerIface").Description; d != "" {
		t.Errorf("containerIface: %q", d)
	}
	if d := mustParam(t, s, "lanNet").Description; strings.Contains(d, "PARAMETERS") {
		t.Errorf("lanNet has the marker: %q", d)
	}
	// a blank line between a comment and its :local cuts it off; annotations
	// are not part of the description
	s = params.Parse(block("# far away", "", "# near", "# @secret", ":local pass \"x\""))
	if p := mustParam(t, s, "pass"); p.Description != "near" || !p.Annotations.Secret {
		t.Errorf("pass: %+v", p)
	}
}

func TestDuplicateParameter(t *testing.T) {
	b := paramstest.Replace(paramstest.WithEnd(paramstest.Today()), ":local wanIface \"ether1\"\n", ":local wanIface \"ether1\"\n:local lanNet \"10.1.1\"\n")
	s := params.Parse(b)
	errs := s.Errors()
	if len(errs) != 1 || errs[0].Line != 27 || errs[0].Key != "params.err.duplicate" {
		t.Fatalf("errors: %s", keys(errs))
	}
	if got := text(t, errs[0]); got != "Duplicate parameter lanNet." {
		t.Errorf("text: %q", got)
	}
	if mustParam(t, s, "lanNet").Default != "10.230.1" {
		t.Error("the first lanNet must stay")
	}
}

func TestNoParametersBlock(t *testing.T) {
	s := params.Parse([]byte("# just a script\n:local x \"1\"\n/system identity set name=x\n"))
	if s.Block || len(s.Params) != 0 || keys(s.Findings) != "0:warning:params.warn.no_block" {
		t.Fatalf("no block: %+v", s)
	}
	if got := text(t, s.Findings[0]); got != "No PARAMETERS block: the script is versioned and downloadable, with nothing to fill in." {
		t.Errorf("text: %q", got)
	}
	s = params.Parse([]byte(":put a\n# END PARAMETERS\n"))
	if keys(s.Findings) != "0:warning:params.warn.no_block 2:warning:params.warn.end_without_block" {
		t.Errorf("end without a block: %s", keys(s.Findings))
	}
	// an end marker before the block is a warning too
	s = params.Parse([]byte("# END PARAMETERS\n# PARAMETERS\n:local a \"1\"\n# END PARAMETERS\n"))
	if keys(s.Findings) != "1:warning:params.warn.end_without_block" || len(s.Params) != 1 {
		t.Errorf("end before the block: %s", keys(s.Findings))
	}
}

func TestBareLiteral(t *testing.T) {
	s := params.Parse(block(":local retries 3", ":local on true", ":local port ether1"))
	if len(s.Findings) != 0 {
		t.Fatalf("findings: %s", keys(s.Findings))
	}
	p := mustParam(t, s, "retries")
	if !p.Bare || p.Default != "3" || p.Literal != "3" {
		t.Errorf("retries: %+v", p)
	}
	if mustParam(t, s, "on").Default != "true" || mustParam(t, s, "port").Default != "ether1" {
		t.Error("bare words")
	}
}

func TestBlockLines(t *testing.T) {
	for _, marker := range []string{"#PARAMETERS", "#   PARAMETERS  ", "# PARAMETERS\t"} {
		s := params.Parse([]byte("#   1. Edit the PARAMETERS section below.\n" + marker + "\n:local a \"1\"\n# END PARAMETERS\n"))
		if !s.Block || s.Start != 2 || len(s.Params) != 1 || len(s.Findings) != 0 {
			t.Errorf("%q: start %d, %s", marker, s.Start, keys(s.Findings))
		}
	}
	for _, notOne := range []string{" # PARAMETERS", "# parameters", "# PARAMETERS section", "#   1. Edit the PARAMETERS section below."} {
		if s := params.Parse([]byte(notOne + "\n:local a \"1\"\n")); s.Block {
			t.Errorf("%q started a block", notOne)
		}
	}
	// a second marker is an error; code inside an ended block a warning;
	// an unreadable :local an error
	s := params.Parse(block(":local a \"1\"", "# PARAMETERS", "/ip address print", ":local 9x \"1\"", ":local b=\"2\"", ":local c \"bad\\q\"", ":local d \"open"))
	if got := keys(s.Findings); got != "3:error:params.err.second_block 4:warning:params.warn.code_in_block 5:error:params.err.unreadable 6:error:params.err.unreadable 7:error:params.err.unreadable 8:error:params.err.unreadable" {
		t.Errorf("findings: %s", got)
	}
	if got := text(t, s.Findings[1]); got != "Line 4 inside the PARAMETERS block is neither a comment nor a :local line." {
		t.Errorf("code: %q", got)
	}
	if got := text(t, s.Findings[2]); got != "Line 5: can't read this :local line." {
		t.Errorf("unreadable: %q", got)
	}
	// CRLF lines read like LF
	crlf := []byte(strings.ReplaceAll(string(paramstest.WithEnd(paramstest.Today())), "\n", "\r\n"))
	s = params.Parse(crlf)
	if len(s.Findings) != 0 || !slices.Equal(names(s), todaysNames) || mustParam(t, s, "lanNet").Literal != `"10.230.1"` || s.End != 58 {
		t.Errorf("CRLF: %s %v", keys(s.Findings), names(s))
	}
	// the block runs to the end of the file without a marker
	s = params.Parse([]byte("# PARAMETERS\n:local a \"1\"\n\n# b\n:local b 2"))
	if s.Ended || s.End != 5 || len(s.Params) != 2 || keys(s.Findings) != "5:warning:params.warn.no_end" {
		t.Errorf("to the end: %d %s", s.End, keys(s.Findings))
	}
}

func TestGoneParameter(t *testing.T) {
	v3 := params.Parse(paramstest.WithEnd(paramstest.Today()))
	draft := params.Parse(paramstest.Replace(paramstest.WithEnd(paramstest.Today()), ":local vethName \"vless\"\n", ""))
	gone := params.Gone(v3, 3, draft)
	if len(gone) != 1 || gone[0].Severity != params.Warning {
		t.Fatalf("gone: %+v", gone)
	}
	if got := text(t, gone[0]); got != "vethName was in v3 and is gone here." {
		t.Errorf("text: %q", got)
	}
	if g := params.Gone(v3, 3, v3); len(g) != 0 {
		t.Errorf("nothing gone: %+v", g)
	}
}

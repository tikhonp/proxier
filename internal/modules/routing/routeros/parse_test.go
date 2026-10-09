package routeros

import (
	"slices"
	"strings"
	"testing"
)

func TestParseScriptInvertsBlocks(t *testing.T) {
	n := Names{List: "vpn_list", Forwarder: "doh"}
	entries := []Entry{{Name: "a.com"}, {Name: "b.a.com", Exact: true}, {Name: "xn--e1afmkfd.xn--p1ai"}}
	var body []string
	body = append(body, "# header", "")
	body = append(body, ServiceBlock(n, "mine", entries, false)...)
	body = append(body, "# continued")
	body = append(body, ServiceBlock(n, "big", entries[:1], true)...)
	body = append(body, RemovalBlock(n, "old")...)
	ops, err := ParseScript([]byte(strings.Join(body, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 {
		t.Fatalf("ops: %+v", ops)
	}
	sorted := append([]Entry(nil), entries...)
	Sort(sorted)
	if o := ops[0]; o.Kind != "update" || o.Tag != "mine" || o.Names != n || !slices.Equal(o.Entries, sorted) {
		t.Fatalf("update: %+v", o)
	}
	if o := ops[1]; o.Kind != "continue" || o.Tag != "big" || len(o.Entries) != 1 {
		t.Fatalf("continue: %+v", o)
	}
	if o := ops[2]; o.Kind != "remove" || o.Tag != "old" || o.Names.List != "vpn_list" {
		t.Fatalf("remove: %+v", o)
	}

	block := ServiceBlock(n, "mine", entries, false)
	for _, bad := range []string{
		"/system reboot",
		strings.Join(block[:len(block)-1], "\n"),       // never ends
		strings.Join(RemovalBlock(n, "old")[:1], "\n"), // half a removal
		strings.Replace(strings.Join(block, "\n"), `:set ($ours->"a.com") 1`, "", 1),               // a name missing from the array
		strings.Replace(strings.Join(block, "\n"), "match-subdomain=yes", "match-subdomain=no", 1), // add ≠ array order
		strings.Replace(strings.Join(block, "\n"), "/ip firewall address-list remove $e", "/ip firewall address-list remove", 1),
		"{\n}",
	} {
		if _, err := ParseScript([]byte(bad)); err == nil {
			t.Fatalf("accepted:\n%s", bad)
		}
	}
}

func TestParseCommands(t *testing.T) {
	n := Names{List: "to_vpn_list", Forwarder: "vpn-doh"}
	o := Names{List: "other.list", Forwarder: "doh2"}
	for _, c := range []struct {
		cmd  string
		want Call
	}{
		{CmdCheck(n), Call{Kind: "check", Names: n}},
		{CmdCheck(o), Call{Kind: "check", Names: o}},
		{CmdReadDNS(o), Call{Kind: "read-dns", Names: Names{List: o.List}}},
		{CmdReadList(n), Call{Kind: "read-list", Names: Names{List: n.List}}},
		{CmdImport("proxier-sync-12-1.rsc"), Call{Kind: "import", File: "proxier-sync-12-1.rsc"}},
		{CmdRemoveFile("proxier-sync-12-1.rsc"), Call{Kind: "remove-file", File: "proxier-sync-12-1.rsc"}},
		{CmdRemoveJobFiles("2207"), Call{Kind: "remove-job-files", File: "2207"}},
		{CmdRemoveJobFiles("preview"), Call{Kind: "remove-job-files", File: "preview"}},
	} {
		got, ok := Parse(c.cmd)
		if !ok || got != c.want {
			t.Fatalf("%s: %+v %v", c.cmd, got, ok)
		}
	}
	mixed := strings.Replace(CmdCheck(n), `list="to_vpn_list" dynamic=no`, `list="other" dynamic=no`, 1)
	for _, bad := range []string{"", "/system reboot", CmdImport("x.rsc") + "; /system reboot", mixed, "/import file-name=a b verbose=no", strings.ToUpper(CmdReadDNS(n))} {
		if c, ok := Parse(bad); ok {
			t.Fatalf("recognised %q as %+v", bad, c)
		}
	}
}

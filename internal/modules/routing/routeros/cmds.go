package routeros

import (
	"fmt"
	"regexp"
	"strings"
)

// The commands, as templates: %[1]s is the address list, %[2]s the
// forwarder, %[3]s a file or a job. Parse matches the same templates.
const (
	tmplCheck = `:put ("version|" . [/system resource get version]); :put ("board|" . [/system resource get board-name]); ` +
		`:put ("identity|" . [/system identity get name]); :put ("forwarder|" . [:len [/ip dns forwarders find where name="%[2]s"]]); ` +
		`:put ("pin|" . [:len [/ip firewall address-list find where list="%[1]s" comment="mtvpn:doh"]]); ` +
		`:put ("entries|" . [:len [/ip firewall address-list find where list="%[1]s" dynamic=no]])`
	// One print instead of a get per field per entry: with 3,226 names a
	// CHR read its DNS entries in 3.3 s instead of 23 s. A bare print
	// as-value leaves out match-subdomain and forward-to, so proplist
	// names every field read.
	tmplReadDNS = `:foreach e in=[/ip dns static print as-value proplist=comment,name,match-subdomain,type,forward-to where address-list="%[1]s"] do={:put ([:tostr ($e->"comment")] . "|" . ` +
		`[:tostr ($e->"name")] . "|" . [:tostr ($e->"match-subdomain")] . "|" . ` +
		`[:tostr ($e->"type")] . "|" . [:tostr ($e->"forward-to")])}`
	tmplReadList = `:foreach e in=[/ip firewall address-list print as-value proplist=comment,address where list="%[1]s" dynamic=no] do={:put ([:tostr ` +
		`($e->"comment")] . "|" . [:tostr ($e->"address")])}`
	tmplImport          = `/import file-name=%[3]s verbose=no`
	tmplRemoveFile      = `:do {/file remove [find name="%[3]s"]} on-error={}`
	tmplRemoveSyncFiles = `:do {/file remove [find name~"^proxier-sync-"]} on-error={}`
)

func cmd(tmpl string, n Names, file string) string {
	return fmt.Sprintf(tmpl, n.List, n.Forwarder, file)
}

// CmdCheck prints key|value lines: version, board, identity, the forwarder's
// count, the mtvpn:doh pin's count and the address list's static entries.
func CmdCheck(n Names) string { return cmd(tmplCheck, n, "") }

// CmdReadDNS prints comment|name|match-subdomain|type|forward-to for every
// DNS static entry of the address list.
func CmdReadDNS(n Names) string { return cmd(tmplReadDNS, n, "") }

// CmdReadList prints comment|address for every static entry of the address list.
func CmdReadList(n Names) string { return cmd(tmplReadList, n, "") }

// CmdImport runs an uploaded script.
func CmdImport(file string) string { return cmd(tmplImport, Names{}, file) }

// CmdRemoveFile deletes a file; a missing one is no error.
func CmdRemoveFile(file string) string { return cmd(tmplRemoveFile, Names{}, file) }

// CmdRemoveSyncFiles deletes every file a sync uploaded, whichever job left it.
func CmdRemoveSyncFiles() string { return tmplRemoveSyncFiles }

// Call is a command Parse recognised.
type Call struct {
	Kind  string // check, read-dns, read-list, import, remove-file, remove-sync-files
	Names Names
	File  string // import, remove-file
}

type pattern struct {
	kind string
	re   *regexp.Regexp
	// which groups hold the list, the forwarder and the file (0: none)
	list, fwd, file int
}

var patterns = func() []pattern {
	mk := func(kind, tmpl string) pattern {
		p := pattern{kind: kind}
		var b strings.Builder
		b.WriteString("^")
		group := 0
		rest := tmpl
		for {
			i := strings.Index(rest, "%[")
			if i < 0 {
				b.WriteString(regexp.QuoteMeta(rest))
				break
			}
			b.WriteString(regexp.QuoteMeta(rest[:i]))
			which := rest[i+2]
			rest = rest[i+5:] // %[n]s
			group++
			switch which {
			case '1':
				// a repeated list is checked by building the command again
				if p.list == 0 {
					p.list = group
				}
				b.WriteString(`([A-Za-z0-9._-]{1,63})`)
			case '2':
				p.fwd = group
				b.WriteString(`([A-Za-z0-9._-]{1,63})`)
			case '3':
				p.file = group
				b.WriteString(`([A-Za-z0-9._-]{1,120})`)
			}
		}
		b.WriteString("$")
		p.re = regexp.MustCompile(b.String())
		return p
	}
	return []pattern{
		mk("check", tmplCheck), mk("read-dns", tmplReadDNS), mk("read-list", tmplReadList),
		mk("import", tmplImport), mk("remove-file", tmplRemoveFile), mk("remove-sync-files", tmplRemoveSyncFiles),
	}
}()

// Parse recognises a command one of the Cmd functions built, and nothing else.
func Parse(c string) (Call, bool) {
	for _, p := range patterns {
		m := p.re.FindStringSubmatch(c)
		if m == nil {
			continue
		}
		call := Call{Kind: p.kind}
		if p.list > 0 {
			call.Names.List = m[p.list]
		}
		if p.fwd > 0 {
			call.Names.Forwarder = m[p.fwd]
		}
		if p.file > 0 {
			call.File = m[p.file]
		}
		// the same command built again, so a repeated name that differs fails
		var again string
		switch p.kind {
		case "check":
			again = CmdCheck(call.Names)
		case "read-dns":
			again = CmdReadDNS(call.Names)
		case "read-list":
			again = CmdReadList(call.Names)
		case "import":
			again = CmdImport(call.File)
		case "remove-file":
			again = CmdRemoveFile(call.File)
		case "remove-sync-files":
			again = CmdRemoveSyncFiles()
		}
		if again != c {
			return Call{}, false
		}
		return call, true
	}
	return Call{}, false
}

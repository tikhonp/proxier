// Package routerostest is a fake RouterOS 7 router (and jump host) on
// sshxtest.Server: the DNS static and address-list tables, files over SFTP,
// and exactly the commands package routeros builds (routeros.Parse and
// routeros.ParseScript are what it understands).
package routerostest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"golang.org/x/crypto/ssh"
)

// Router is a fake RouterOS 7. New routers come set up as fresh-router.rsc
// leaves them: the forwarder vpn-doh, the pins dns.google (mtvpn:doh) and
// core.telegram.org (mtvpn:tg-fetch).
type Router struct {
	Server *sshxtest.Server
	Addr   string

	t  testing.TB
	mu sync.Mutex

	version, board, identity string
	forwarders               map[string]bool
	dns                      []dnsRow
	list                     []listRow

	imports  []string
	bodies   [][]byte
	outputs  map[int]string
	failAt   map[int]failAt
	dropAdds map[string]int
	holds    map[int]chan struct{}
	commands []string
	onImport func(n int)
}

type failAt struct {
	k   int
	out string
}

// New starts a fake router that accepts the authorized key for any user.
func New(t *testing.T, authorized ssh.PublicKey) *Router {
	t.Helper()
	srv := sshxtest.NewServer(t, authorized)
	r := &Router{
		Server: srv, Addr: srv.Addr, t: t,
		version: "7.24.5 (stable)", board: "RB5009UG+S+", identity: "MikroTik",
		forwarders: map[string]bool{routeros.Defaults.Forwarder: true},
		outputs:    map[int]string{}, failAt: map[int]failAt{}, dropAdds: map[string]int{}, holds: map[int]chan struct{}{},
	}
	r.list = []listRow{
		{List: routeros.Defaults.List, Address: "dns.google", Comment: "mtvpn:doh"},
		{List: routeros.Defaults.List, Address: "core.telegram.org", Comment: "mtvpn:tg-fetch"},
	}
	srv.HandleFunc(func(_, cmd string) bool { _, ok := routeros.Parse(cmd); return ok }, r.handle)
	return r
}

// Jump is a jump host that forwards to any address.
func Jump(t *testing.T, authorized ssh.PublicKey) *sshxtest.Server {
	t.Helper()
	s := sshxtest.NewServer(t, authorized)
	s.AllowForwarding()
	return s
}

// SetVersion changes what the check prints.
func (r *Router) SetVersion(version, board string) {
	r.mu.Lock()
	r.version, r.board = version, board
	r.mu.Unlock()
}

func (r *Router) handle(s *sshxtest.Session) int {
	call, _ := routeros.Parse(s.Command)
	r.mu.Lock()
	r.commands = append(r.commands, call.Kind)
	r.mu.Unlock()
	switch call.Kind {
	case "check":
		r.mu.Lock()
		fwd := 0
		if r.forwarders[call.Names.Forwarder] {
			fwd = 1
		}
		pin, entries := 0, 0
		for _, e := range r.list {
			if e.List != call.Names.List || e.Dynamic {
				continue
			}
			entries++
			if e.Comment == "mtvpn:doh" {
				pin++
			}
		}
		out := fmt.Sprintf("version|%s\r\nboard|%s\r\nidentity|%s\r\nforwarder|%d\r\npin|%d\r\nentries|%d\r\n",
			r.version, r.board, r.identity, fwd, pin, entries)
		r.mu.Unlock()
		_, _ = io.WriteString(s.Stdout, out)
	case "read-dns":
		r.mu.Lock()
		var b strings.Builder
		for _, d := range r.dns {
			if d.List == call.Names.List {
				fmt.Fprintf(&b, "%s|%s|%t|%s|%s\r\n", d.Comment, d.Name, d.Subdomain, d.Type, d.ForwardTo)
			}
		}
		r.mu.Unlock()
		_, _ = io.WriteString(s.Stdout, b.String())
	case "read-list":
		r.mu.Lock()
		var b strings.Builder
		for _, e := range r.list {
			if e.List == call.Names.List && !e.Dynamic {
				fmt.Fprintf(&b, "%s|%s\r\n", e.Comment, e.Address)
			}
		}
		r.mu.Unlock()
		_, _ = io.WriteString(s.Stdout, b.String())
	case "import":
		_, _ = io.WriteString(s.Stdout, r.runImport(call.File))
		r.mu.Lock()
		hook, n := r.onImport, len(r.imports)
		r.mu.Unlock()
		if hook != nil {
			hook(n)
		}
	case "remove-file":
		_ = os.Remove(filepath.Join(r.Server.Dir(), call.File))
	case "remove-job-files":
		for _, f := range r.Files() {
			if strings.HasPrefix(f, "proxier-sync-"+call.File+"-") {
				_ = os.Remove(filepath.Join(r.Server.Dir(), f))
			}
		}
	}
	return 0
}

func (r *Router) runImport(file string) string {
	body, err := os.ReadFile(filepath.Join(r.Server.Dir(), file))
	r.mu.Lock()
	r.imports = append(r.imports, file)
	r.bodies = append(r.bodies, body)
	n := len(r.imports)
	hold := r.holds[n]
	r.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if err != nil {
		return "failure: no such file " + file + "\r\n"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if out, ok := r.outputs[n]; ok {
		return out + "\r\n"
	}
	ops, err := routeros.ParseScript(body)
	if err != nil {
		return "syntax error (line 1 column 1)\r\n"
	}
	if f, ok := r.failAt[n]; ok {
		for i, op := range ops {
			if i+1 >= f.k {
				break
			}
			r.apply(op)
		}
		return f.out + "\r\n"
	}
	for _, op := range ops {
		r.apply(op)
	}
	return routeros.Success + "\r\n"
}

// Seed installs a tag's entries as a service block does.
func (r *Router) Seed(tag string, entries ...routeros.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apply(routeros.Op{Kind: "update", Tag: tag, Names: routeros.Defaults, Entries: entries})
}

// SeedUntagged adds a name with no comment, as an old hand-made entry.
func (r *Router) SeedUntagged(name string, exact bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dns = append(r.dns, dnsRow{List: routeros.Defaults.List, DNSEntry: routeros.DNSEntry{
		Name: name, Type: "FWD", ForwardTo: routeros.Defaults.Forwarder, Subdomain: !exact,
	}})
	r.list = append(r.list, listRow{List: routeros.Defaults.List, Address: name})
}

// SeedListOnly adds an address-list entry with no DNS entry (telegram-cidr, a stray one).
func (r *Router) SeedListOnly(comment, address string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, listRow{List: routeros.Defaults.List, Address: address, Comment: comment})
}

// DeleteEntries deletes n of a tag's DNS entries and their address-list
// entries, as someone would by hand.
func (r *Router) DeleteEntries(tag string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gone := map[string]bool{}
	var keep []dnsRow
	for _, d := range r.dns {
		if d.Comment == tag && len(gone) < n {
			gone[d.Name] = true
			continue
		}
		keep = append(keep, d)
	}
	r.dns = keep
	var kl []listRow
	for _, e := range r.list {
		if e.Comment == tag && gone[e.Address] {
			continue
		}
		kl = append(kl, e)
	}
	r.list = kl
}

// EditForwardTo points one entry of a tag at another forwarder, by hand.
func (r *Router) EditForwardTo(tag, name, to string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.dns {
		if r.dns[i].Comment == tag && r.dns[i].Name == name {
			r.dns[i].ForwardTo = to
		}
	}
}

// Files are the files on the router, sorted.
func (r *Router) Files() []string {
	entries, err := os.ReadDir(r.Server.Dir())
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Imports are the imported file names, in order.
func (r *Router) Imports() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.imports...)
}

// Import is the body of the nth (from 1) import.
func (r *Router) Import(n int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n < 1 || n > len(r.bodies) {
		r.t.Fatalf("routerostest: no import %d (%d so far)", n, len(r.bodies))
	}
	return r.bodies[n-1]
}

// Commands are the kinds of commands run, in order.
func (r *Router) Commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

// OnImport calls fn after each import has run (n from 1).
func (r *Router) OnImport(fn func(n int)) {
	r.mu.Lock()
	r.onImport = fn
	r.mu.Unlock()
}

// ImportOutput makes the nth import answer out and apply nothing.
func (r *Router) ImportOutput(n int, out string) {
	r.mu.Lock()
	r.outputs[n] = out
	r.mu.Unlock()
}

// FailAtBlock makes the nth import apply its blocks before the kth (from 1),
// then answer out, as /import stops at the first failing line.
func (r *Router) FailAtBlock(n, k int, out string) {
	r.mu.Lock()
	r.failAt[n] = failAt{k: k, out: out}
	r.mu.Unlock()
}

// NoFTP refuses SFTP writes, as for a group without the ftp policy.
func (r *Router) NoFTP() { r.Server.RefuseUploads(true) }

// DropListAdds makes n address-list adds of the tag fail silently (on-error).
func (r *Router) DropListAdds(tag string, n int) {
	r.mu.Lock()
	r.dropAdds[tag] += n
	r.mu.Unlock()
}

// Hold makes the nth import wait (once it has read its file) until release
// is called; the test's end releases it too.
func (r *Router) Hold(n int) (release func()) {
	ch := make(chan struct{})
	r.mu.Lock()
	r.holds[n] = ch
	r.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	r.t.Cleanup(release)
	return release
}

// NoForwarder removes the DoH forwarder, as on a router the script never set up.
func (r *Router) NoForwarder() {
	r.mu.Lock()
	r.forwarders = map[string]bool{}
	r.mu.Unlock()
}

// Offline refuses connections while off is true.
func (r *Router) Offline(off bool) { r.Server.Refuse(off) }

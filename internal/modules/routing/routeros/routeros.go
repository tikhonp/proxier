// Package routeros is pure: every RouterOS command and script router sync
// sends, the parsers of what the router prints, the scan of /import's output,
// and Parse/ParseScript, their inverses, which the fake router answers
// (docs/integrations/routeros.md). No other package writes RouterOS text.
package routeros

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Names are the router script's names Proxier writes under.
type Names struct {
	List      string `json:"list"`      // the address list, to_vpn_list
	Forwarder string `json:"forwarder"` // the DoH forwarder, vpn-doh
}

// Defaults are fresh-router.rsc's names.
var Defaults = Names{List: "to_vpn_list", Forwarder: "vpn-doh"}

const (
	// InfraPrefix starts the comment of the router script's own pins.
	InfraPrefix = "mtvpn:"
	// TelegramTag is the router script's Telegram CIDR entries (address list only).
	TelegramTag = "telegram-cidr"
	// MaxDomains is the most names one pushed file holds.
	MaxDomains = 2000
	// ImportTimeout bounds one /import: a client timeout mid-import would
	// leave a tag half-installed.
	ImportTimeout = 30 * time.Minute
)

// name is what a list, a forwarder or a file may be called.
var name = regexp.MustCompile(`^[A-Za-z0-9._-]{1,63}$`)

// ValidName reports whether s can be a list or forwarder name.
func ValidName(s string) bool { return name.MatchString(s) }

// Entry is one name of a tag: a suffix (match-subdomain=yes) or exact.
type Entry struct {
	Name  string `json:"name"`
	Exact bool   `json:"exact,omitempty"`
}

// Sort orders entries as blocks write them: suffix names by name, then exact ones.
func Sort(e []Entry) {
	sort.SliceStable(e, func(i, j int) bool {
		if e[i].Exact != e[j].Exact {
			return !e[i].Exact
		}
		return e[i].Name < e[j].Name
	})
}

// EntriesHash is the SHA-256 of the sorted (name, exact) pairs, duplicates
// kept: what a tag's applied state is compared by.
func EntriesHash(e []Entry) string {
	c := append([]Entry(nil), e...)
	Sort(c)
	var b strings.Builder
	for _, x := range c {
		if x.Exact {
			b.WriteString("=")
		} else {
			b.WriteString("+")
		}
		b.WriteString(x.Name)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Counts are the suffix and exact names of entries.
func Counts(e []Entry) (suffix, exact int) {
	for _, x := range e {
		if x.Exact {
			exact++
		} else {
			suffix++
		}
	}
	return suffix, exact
}

// Group is the restricted group Proxier's router user is in (open question
// "to verify" 5: the policies are still to confirm on a real router).
const Group = "proxier"

// KeyCommands create Proxier's group and user on a router and add its key:
// the add router page's commands and a generation's, one text so both change
// together.
func KeyCommands(user, key string) string {
	key = strings.TrimSpace(key)
	return "/user group add name=" + Group + " policy=read,write,ftp,ssh\n" +
		"/user add name=" + user + " group=" + Group + "\n" +
		"/user ssh-keys add user=" + user + " key=\"" + key + "\""
}

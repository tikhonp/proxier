// Package routeros is pure: every RouterOS command and script router sync
// sends, the parsers of what the router prints, the scan of /import's output,
// and Parse/ParseScript, their inverses, which the fake router answers
// (docs/integrations/routeros.md). No other package writes RouterOS text.
package routeros

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
)

// ImportTimeout bounds the /import of a file of names names: a minute, and
// 150 ms per name on top, so a full file (MaxDomains) gets about 6 min. A hEX
// S took about 30 ms per name (64 names in 7.9 s, one in 6 s), so the bound
// is five times that. A client timeout mid-import leaves a tag half-installed
// until the retry, which heals it: the block is idempotent.
func ImportTimeout(names int) time.Duration {
	return time.Minute + time.Duration(names)*150*time.Millisecond
}

// UploadTimeout bounds the upload of a file of size bytes: a minute, and a
// second per KiB on top, so a slow link behind a jump host still carries a
// full file (MaxDomains names, about 530 KiB) while a dead one is given up on.
func UploadTimeout(size int) time.Duration {
	return time.Minute + time.Duration(size/1024)*time.Second
}

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

// RandomPassword is a RouterOS expression for a 32-character password.
const RandomPassword = `[:rndstr length=32 from="abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"]`

// KeyCommands create Proxier's group and user on a router and add its key:
// the add router page's commands and a generation's, one text so both change
// together. The user gets a random password nobody knows: without one,
// /user add asks for it in the terminal, so the pasted lines stop, and
// Winbox and the console would accept an empty one.
func KeyCommands(user, key string) string {
	key = strings.TrimSpace(key)
	return "/user group add name=" + Group + " policy=read,write,ftp,ssh\n" +
		"/user add name=" + user + " group=" + Group + " password=" + RandomPassword + "\n" +
		"/user ssh-keys add user=" + user + " key=\"" + key + "\""
}

// Quote escapes s for a RouterOS "…" string. Service tags pass unchanged; an
// unmanaged tag is a comment read from the router, and its quote, backslash,
// "$" (a variable) or a control character would otherwise end the string or
// the line and run the rest as script.
func Quote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\' || c == '$' || c == '?':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\%02X`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Unquote reverses Quote.
func Unquote(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		if i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i+1])
		i++
	}
	return b.String()
}

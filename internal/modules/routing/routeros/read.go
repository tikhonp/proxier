package routeros

import (
	"fmt"
	"strconv"
	"strings"
)

// Check is what CmdCheck printed.
type Check struct {
	Version, Board, Identity string
	Forwarder, Pin, Entries  int
}

// DNSEntry is one DNS static entry of the address list.
type DNSEntry struct {
	Comment, Name, Type, ForwardTo string
	Subdomain                      bool
}

// ListEntry is one static address-list entry.
type ListEntry struct{ Comment, Address string }

// lines splits output into lines without their \r, skipping blank ones.
func lines(out []byte) []string {
	var res []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		res = append(res, l)
	}
	return res
}

// ParseCheck reads CmdCheck's output.
func ParseCheck(out []byte) (Check, error) {
	var c Check
	seen := map[string]bool{}
	for _, l := range lines(out) {
		k, v, ok := strings.Cut(l, "|")
		if !ok {
			continue // SSH noise
		}
		v = strings.TrimSpace(v)
		num := func() (int, error) {
			n, err := strconv.Atoi(v)
			if err != nil {
				return 0, fmt.Errorf("routeros: %s is %q, not a number", k, v)
			}
			return n, nil
		}
		var err error
		switch k {
		case "version":
			c.Version = v
		case "board":
			c.Board = v
		case "identity":
			c.Identity = v
		case "forwarder":
			c.Forwarder, err = num()
		case "pin":
			c.Pin, err = num()
		case "entries":
			c.Entries, err = num()
		default:
			continue
		}
		if err != nil {
			return Check{}, err
		}
		seen[k] = true
	}
	for _, k := range []string{"version", "board", "forwarder", "pin", "entries"} {
		if !seen[k] {
			return Check{}, fmt.Errorf("routeros: the check printed no %s", k)
		}
	}
	return c, nil
}

// ParseDNS reads CmdReadDNS's output. Fields are split from the right: a
// comment written by hand may hold "|".
func ParseDNS(out []byte) ([]DNSEntry, error) {
	var res []DNSEntry
	for _, l := range lines(out) {
		f := strings.Split(l, "|")
		if len(f) < 5 {
			return nil, fmt.Errorf("routeros: unexpected DNS line %q", l)
		}
		n := len(f)
		e := DNSEntry{
			Comment: strings.Join(f[:n-4], "|"), Name: f[n-4], Type: f[n-2], ForwardTo: f[n-1],
		}
		switch f[n-3] {
		case "true", "yes":
			e.Subdomain = true
		case "false", "no", "":
		default:
			return nil, fmt.Errorf("routeros: unexpected match-subdomain %q", f[n-3])
		}
		res = append(res, e)
	}
	return res, nil
}

// ParseList reads CmdReadList's output.
func ParseList(out []byte) ([]ListEntry, error) {
	var res []ListEntry
	for _, l := range lines(out) {
		i := strings.LastIndexByte(l, '|')
		if i < 0 {
			return nil, fmt.Errorf("routeros: unexpected address-list line %q", l)
		}
		res = append(res, ListEntry{Comment: l[:i], Address: l[i+1:]})
	}
	return res, nil
}

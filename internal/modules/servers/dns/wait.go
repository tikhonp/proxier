package dns

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Resolver is a public resolver to ask.
type Resolver struct {
	Name string // "Cloudflare", "Google"
	DoH  string // "https://1.1.1.1/dns-query"
	UDP  string // "1.1.1.1:53": the fallback
}

// DefaultResolvers are the two Proxier waits for. Proxier sits behind a home
// router that may intercept port 53, so they are asked over DNS-over-HTTPS at
// IP-addressed URLs (no DNS needed to find them).
var DefaultResolvers = []Resolver{
	{Name: "Cloudflare", DoH: "https://1.1.1.1/dns-query", UDP: "1.1.1.1:53"},
	{Name: "Google", DoH: "https://8.8.8.8/dns-query", UDP: "8.8.8.8:53"},
}

// Log is where the wait reports; the job's logger satisfies it.
type Log interface {
	Info(format string, args ...any)
	Warn(format string, args ...any)
}

type nopLog struct{}

func (nopLog) Info(string, ...any) {}
func (nopLog) Warn(string, ...any) {}

// Waiter waits until every resolver answers a name with an IP.
type Waiter struct {
	Resolvers []Resolver
	Every     time.Duration // between rounds (10 s)
	Timeout   time.Duration // in all (10 min)
	HTTP      *http.Client
	// Lookup replaces both transports in tests; transport is "doh" or "udp".
	Lookup func(ctx context.Context, r Resolver, name string) (addrs []netip.Addr, transport string, err error)
}

// NewWaiter is the production waiter.
func NewWaiter() Waiter {
	return Waiter{Resolvers: DefaultResolvers, Every: 10 * time.Second, Timeout: 10 * time.Minute,
		HTTP: &http.Client{Timeout: 8 * time.Second}}
}

// Wait returns nil when every resolver answers name with ip, ErrNotVisible
// (naming what each answered) after Timeout, or the context's error at once
// when it is cancelled.
func (w Waiter) Wait(ctx context.Context, name, ip string, log Log) error {
	if log == nil {
		log = nopLog{}
	}
	want, err := netip.ParseAddr(ip)
	if err != nil {
		return fmt.Errorf("dns: %q is not an IP address", ip)
	}
	every, timeout := w.Every, w.Timeout
	if every <= 0 {
		every = 10 * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	lookup := w.Lookup
	if lookup == nil {
		lookup = w.lookup
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTimer(0)
	defer tick.Stop()

	// last is what each resolver last said, and over which transport.
	last, lastVia := map[string]string{}, map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return w.notVisible(last)
		case <-tick.C:
		}
		ok := true
		for _, r := range w.Resolvers {
			addrs, transport, err := lookup(ctx, r, name)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var said string
			switch {
			case err != nil:
				said = "failed: " + err.Error()
				ok = false
			case contains(addrs, want):
				said = "answered " + want.String()
			default:
				said = "answered " + describe(addrs)
				ok = false
			}
			// A line when the answer changes, so a 10-minute wait stays readable.
			if last[r.Name] != said || lastVia[r.Name] != transport {
				last[r.Name], lastVia[r.Name] = said, transport
				log.Info("%s (%s) %s", r.Name, transport, said)
			}
		}
		if ok {
			return nil
		}
		tick.Reset(every)
	}
}

func (w Waiter) notVisible(last map[string]string) error {
	var parts []string
	for _, r := range w.Resolvers {
		said := last[r.Name]
		if said == "" {
			said = "gave no answer"
		}
		parts = append(parts, r.Name+" "+said)
	}
	return fmt.Errorf("%w: %s", ErrNotVisible, strings.Join(parts, "; "))
}

func contains(addrs []netip.Addr, a netip.Addr) bool {
	for _, x := range addrs {
		if x == a {
			return true
		}
	}
	return false
}

func describe(addrs []netip.Addr) string {
	if len(addrs) == 0 {
		return "nothing"
	}
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	sort.Strings(s)
	return strings.Join(s, ", ")
}

// lookup asks over DoH, and over plain UDP when the DoH request itself fails.
func (w Waiter) lookup(ctx context.Context, r Resolver, name string) ([]netip.Addr, string, error) {
	addrs, err := w.doh(ctx, r, name)
	if err == nil {
		return addrs, "doh", nil
	}
	if ctx.Err() != nil {
		return nil, "doh", ctx.Err()
	}
	if r.UDP == "" {
		return nil, "doh", err
	}
	addrs, uerr := udp(ctx, r, name)
	if uerr != nil {
		return nil, "udp", fmt.Errorf("DoH: %v; UDP: %v", err, uerr)
	}
	return addrs, "udp", nil
}

// doh is RFC 8484: a GET with the query in the dns parameter.
func (w Waiter) doh(ctx context.Context, r Resolver, name string) ([]netip.Addr, error) {
	n, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	wire, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.DoH+"?dns="+base64.RawURLEncoding.EncodeToString(wire), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-message")
	client := w.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	var ans dnsmessage.Message
	if err := ans.Unpack(body); err != nil {
		return nil, fmt.Errorf("unreadable DNS answer: %w", err)
	}
	switch ans.RCode {
	case dnsmessage.RCodeSuccess, dnsmessage.RCodeNameError:
	default:
		return nil, fmt.Errorf("DNS %s", ans.RCode)
	}
	var out []netip.Addr
	for _, rr := range ans.Answers {
		if a, ok := rr.Body.(*dnsmessage.AResource); ok {
			out = append(out, netip.AddrFrom4(a.A))
		}
	}
	return out, nil
}

// udp asks the resolver's port 53 directly.
func udp(ctx context.Context, r Resolver, name string) ([]netip.Addr, error) {
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "udp", r.UDP)
	}}
	addrs, err := res.LookupNetIP(ctx, "ip4", name)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil // an answer: the name does not exist (yet)
	}
	return addrs, err
}

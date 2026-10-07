package dns_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	waitName = "nl-1.hosts.tikhonnnnn.com"
	waitIP   = "198.51.100.7"
)

type memLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *memLog) Info(f string, a ...any) { l.add(fmt.Sprintf(f, a...)) }
func (l *memLog) Warn(f string, a ...any) { l.add("WARN " + fmt.Sprintf(f, a...)) }
func (l *memLog) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
}
func (l *memLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func fastWaiter(rs ...dns.Resolver) dns.Waiter {
	return dns.Waiter{Resolvers: rs, Every: 5 * time.Millisecond, Timeout: 300 * time.Millisecond}
}

func TestWaitForBothResolvers(t *testing.T) {
	cf := dns.Resolver{Name: "Cloudflare"}
	g := dns.Resolver{Name: "Google"}
	ip := netip.MustParseAddr(waitIP)
	var mu sync.Mutex
	googleSeesIt := 0 // the round from which Google answers with the IP

	lookup := func(_ context.Context, r dns.Resolver, _ string) ([]netip.Addr, string, error) {
		mu.Lock()
		defer mu.Unlock()
		if r.Name == "Google" {
			googleSeesIt--
			if googleSeesIt > 0 {
				return nil, "doh", nil
			}
		}
		return []netip.Addr{ip}, "doh", nil
	}

	// both answer on the third round
	googleSeesIt = 3
	w := fastWaiter(cf, g)
	w.Lookup = lookup
	log := &memLog{}
	if err := w.Wait(t.Context(), waitName, waitIP, log); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(log.text(), "Cloudflare (doh) answered "+waitIP) || !strings.Contains(log.text(), "Google (doh) answered nothing") {
		t.Errorf("log:\n%s", log.text())
	}

	// one stays stale: the error names who answered what
	googleSeesIt = 1 << 30
	w.Lookup = func(ctx context.Context, r dns.Resolver, n string) ([]netip.Addr, string, error) {
		if r.Name == "Google" {
			return []netip.Addr{netip.MustParseAddr("203.0.113.1")}, "doh", nil
		}
		return lookup(ctx, r, n)
	}
	start := time.Now()
	err := w.Wait(t.Context(), waitName, waitIP, nil)
	if !errors.Is(err, dns.ErrNotVisible) {
		t.Fatalf("stale: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "Google answered 203.0.113.1") || !strings.Contains(msg, "Cloudflare answered "+waitIP) {
		t.Errorf("the error should say who saw what: %v", err)
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 2*time.Second {
		t.Errorf("gave up after %s", d)
	}

	// a resolver that fails every time is named as failing
	w.Lookup = func(_ context.Context, r dns.Resolver, _ string) ([]netip.Addr, string, error) {
		if r.Name == "Google" {
			return nil, "udp", errors.New("i/o timeout")
		}
		return []netip.Addr{ip}, "doh", nil
	}
	if err := w.Wait(t.Context(), waitName, waitIP, nil); !errors.Is(err, dns.ErrNotVisible) || !strings.Contains(err.Error(), "Google failed: i/o timeout") {
		t.Errorf("failing resolver: %v", err)
	}

	// a cancel returns at once, long before the timeout
	slow := dns.Waiter{Resolvers: []dns.Resolver{cf}, Every: time.Hour, Timeout: time.Hour,
		Lookup: func(context.Context, dns.Resolver, string) ([]netip.Addr, string, error) { return nil, "doh", nil }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- slow.Wait(ctx, waitName, waitIP, nil) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait ignored the cancel")
	}

	if err := w.Wait(t.Context(), waitName, "not-an-ip", nil); err == nil {
		t.Error("a bad IP was accepted")
	}
}

// dohServer answers RFC 8484 GET queries with the A records in answers[name];
// a name it does not know is NXDOMAIN.
func dohServer(t *testing.T, answers map[string]string) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		if r.Header.Get("Accept") != "application/dns-message" || r.Method != http.MethodGet {
			http.Error(w, "bad request", 400)
			return
		}
		wire, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil {
			http.Error(w, "bad dns", 400)
			return
		}
		var q dnsmessage.Message
		if err := q.Unpack(wire); err != nil || len(q.Questions) != 1 || q.Questions[0].Type != dnsmessage.TypeA {
			http.Error(w, "bad question", 400)
			return
		}
		name := strings.TrimSuffix(q.Questions[0].Name.String(), ".")
		resp := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: q.ID, Response: true, RecursionAvailable: true},
			Questions: q.Questions,
		}
		if ip, ok := answers[name]; ok {
			a := netip.MustParseAddr(ip).As4()
			resp.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: a},
			}}
		} else {
			resp.RCode = dnsmessage.RCodeNameError
		}
		out, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// udpServer answers A queries on a local UDP port with ip (or NXDOMAIN when empty).
func udpServer(t *testing.T, ip string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) != 1 {
				continue
			}
			resp := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, RecursionAvailable: true}, Questions: q.Questions}
			if q.Questions[0].Type == dnsmessage.TypeA && ip != "" {
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: netip.MustParseAddr(ip).As4()},
				}}
			} else if ip == "" {
				resp.RCode = dnsmessage.RCodeNameError
			}
			out, _ := resp.Pack()
			_, _ = pc.WriteTo(out, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestWaitOverDoHWithUDPFallback(t *testing.T) {
	good, goodHits := dohServer(t, map[string]string{waitName: waitIP})
	empty, _ := dohServer(t, nil)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // nothing listens: the DoH request fails at the network level
	udp := udpServer(t, waitIP)

	// DoH works for both
	w := fastWaiter(
		dns.Resolver{Name: "Cloudflare", DoH: good.URL, UDP: "127.0.0.1:1"},
		dns.Resolver{Name: "Google", DoH: good.URL, UDP: "127.0.0.1:1"},
	)
	w.HTTP = good.Client()
	log := &memLog{}
	if err := w.Wait(t.Context(), waitName, waitIP, log); err != nil {
		t.Fatalf("DoH: %v", err)
	}
	if *goodHits < 2 || !strings.Contains(log.text(), "Cloudflare (doh) answered "+waitIP) || !strings.Contains(log.text(), "Google (doh) answered "+waitIP) {
		t.Errorf("hits %d, log:\n%s", *goodHits, log.text())
	}

	// NXDOMAIN over DoH is an answer ("nothing"), not a failure: no UDP fallback
	w = fastWaiter(dns.Resolver{Name: "Cloudflare", DoH: empty.URL, UDP: udp})
	w.HTTP = empty.Client()
	log = &memLog{}
	err := w.Wait(t.Context(), waitName, waitIP, log)
	if !errors.Is(err, dns.ErrNotVisible) || !strings.Contains(err.Error(), "Cloudflare answered nothing") || strings.Contains(log.text(), "udp") {
		t.Errorf("NXDOMAIN: %v\n%s", err, log.text())
	}

	// a DoH that cannot be reached: the same resolver is asked over UDP, and the log says so
	w = fastWaiter(dns.Resolver{Name: "Google", DoH: deadURL, UDP: udp})
	log = &memLog{}
	if err := w.Wait(t.Context(), waitName, waitIP, log); err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if !strings.Contains(log.text(), "Google (udp) answered "+waitIP) {
		t.Errorf("the log should name the transport:\n%s", log.text())
	}

	// both transports down: the failure names both
	w = fastWaiter(dns.Resolver{Name: "Google", DoH: deadURL, UDP: "127.0.0.1:1"})
	w.Timeout = 100 * time.Millisecond
	err = w.Wait(t.Context(), waitName, waitIP, nil)
	if !errors.Is(err, dns.ErrNotVisible) || !strings.Contains(err.Error(), "DoH:") || !strings.Contains(err.Error(), "UDP:") {
		t.Errorf("both down: %v", err)
	}

	// a DoH server that answers HTTP 500 also falls back
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }))
	t.Cleanup(broken.Close)
	w = fastWaiter(dns.Resolver{Name: "Cloudflare", DoH: broken.URL, UDP: udp})
	w.HTTP = broken.Client()
	log = &memLog{}
	if err := w.Wait(t.Context(), waitName, waitIP, log); err != nil || !strings.Contains(log.text(), "(udp)") {
		t.Errorf("HTTP 500: %v\n%s", err, log.text())
	}
}

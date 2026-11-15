package aicheck

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestReputationUsesHTTP1WhenServerOffersHTTP2(t *testing.T) {
	h := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("reputation request negotiated %s", r.Proto)
		}
		_, _ = io.WriteString(w, `{"ip":"8.8.8.8","trust_score":63,"countryCode":"us"}`)
	}))
	h.EnableHTTP2 = true
	h.StartTLS()
	defer h.Close()

	hc := New(Deps{}).HTTP
	transport := hc.Transport.(*http.Transport)
	transport.TLSClientConfig = h.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	defer transport.CloseIdleConnections()
	for range 2 {
		r, _, err := lookup(t.Context(), hc, h.URL, netip.MustParseAddr("8.8.8.8"), time.Now)
		if err != nil || r.Score == nil || *r.Score != 63 {
			t.Fatalf("%+v %v", r, err)
		}
	}
}

func TestLookupValidation(t *testing.T) {
	for _, body := range []string{
		`{`, `null`, `{}`, `{"ip":"8.8.8.8"}`, `{"ip":"8.8.8.8","trust_score":null}`,
		`{"ip":"8.8.8.8","trust_score":"63"}`, `{"ip":"8.8.8.8","trust_score":-1}`, `{"ip":"8.8.8.8","trust_score":101}`,
		`{"ip":"1.1.1.1","trust_score":63}`, `{"ip":"8.8.8.8","trust_score":63} {}`, strings.Repeat("x", (64<<10)+1),
	} {
		t.Run(body[:min(len(body), 60)], func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer h.Close()
			if _, _, err := lookup(t.Context(), h.Client(), h.URL, netip.MustParseAddr("8.8.8.8"), time.Now); err == nil {
				t.Fatal("invalid reputation accepted")
			}
		})
	}
	for _, ip := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		t.Run(ip, func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/iprisk/"+ip {
					t.Errorf("path %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"ip":"`+ip+`","trust_score":0,"countryCode":"ru","is_vpn":null,"is_proxy":false}`)
			}))
			defer h.Close()
			r, _, err := lookup(t.Context(), h.Client(), h.URL, netip.MustParseAddr(ip), time.Now)
			if err != nil || r.Score == nil || *r.Score != 0 || r.Country != "RU" || r.VPN != nil || r.Proxy == nil || *r.Proxy {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestLookupCIDRCachedResponse(t *testing.T) {
	for _, tt := range []struct {
		name, queried, returned, cidr string
		valid                         bool
	}{
		{"production", "87.121.82.109", "87.121.82.209", "87.121.82.0/24", true},
		{"ipv6", "2606:4700:4700::1111", "2606:4700:4700::1001", "2606:4700:4700::/48", true},
		{"exact IP", "8.8.8.8", "8.8.8.8", "8.8.8.0/24", true},
		{"mapped returned IP", "8.8.8.8", "::ffff:8.8.8.9", "8.8.8.0/24", true},
		{"missing CIDR", "87.121.82.109", "87.121.82.209", "", false},
		{"malformed CIDR", "87.121.82.109", "87.121.82.209", "bad", false},
		{"noncanonical CIDR", "87.121.82.109", "87.121.82.209", "87.121.82.109/24", false},
		{"queried IP outside CIDR", "8.8.8.8", "87.121.82.209", "87.121.82.0/24", false},
		{"returned IP outside CIDR", "87.121.82.109", "8.8.8.8", "87.121.82.0/24", false},
		{"private returned IP", "8.8.8.8", "10.0.0.1", "0.0.0.0/0", false},
		{"unspecified range", "8.8.8.8", "1.1.1.1", "0.0.0.0/0", false},
		{"wrong address family", "8.8.8.8", "8.8.8.8", "2606:4700:4700::/48", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"ip":"`+tt.returned+`","cidr":"`+tt.cidr+`","trust_score":86,"countryCode":"fi"}`)
			}))
			defer h.Close()
			r, _, err := lookup(t.Context(), h.Client(), h.URL, netip.MustParseAddr(tt.queried), time.Now)
			if !tt.valid {
				if err == nil {
					t.Fatal("unrelated reputation accepted")
				}
				return
			}
			if err != nil || r.IP != tt.queried || r.CIDR != tt.cidr || r.Score == nil || *r.Score != 86 {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestTraceValidation(t *testing.T) {
	for _, body := range []string{"", "ip=", "ip=bad", "ip=127.0.0.1", "ip=10.0.0.1", "ip=100.64.0.1", "ip=::1", "ip=fe80::1%en0", "ip=192.0.2.1", "ip=240.1.1.1", "ip=2001:db8::1", "ip=8.8.8.8\nip=1.1.1.1", strings.Repeat("x", 8193)} {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		_, _, err := observe(t.Context(), h.Client(), h.URL)
		h.Close()
		if err == nil {
			t.Errorf("accepted %q", body[:min(len(body), 60)])
		}
	}
}

func TestLookupRateLimitAndBoundedRetries(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	for _, header := range []string{"7200", now.Add(2 * time.Hour).Format(http.TimeFormat), "bad"} {
		calls := 0
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Retry-After", header)
			w.WriteHeader(429)
		}))
		_, until, err := lookup(t.Context(), h.Client(), h.URL, netip.MustParseAddr("8.8.8.8"), func() time.Time { return now })
		h.Close()
		want := now.Add(2 * time.Hour)
		if header == "bad" {
			want = now.Add(time.Hour)
		}
		if err == nil || calls != 1 || !until.Equal(want) {
			t.Fatalf("429: %d %v %v", calls, until, err)
		}
	}
	calls := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer h.Close()
	_, _, err := lookup(t.Context(), h.Client(), h.URL, netip.MustParseAddr("8.8.8.8"), time.Now)
	if err == nil || calls != 2 {
		t.Fatalf("503 calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := lookup(ctx, h.Client(), h.URL, netip.MustParseAddr("8.8.8.8"), time.Now); err == nil {
		t.Fatal("cancel ignored")
	}
}

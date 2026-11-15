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

package httpx_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/httpx"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func get(e *echo.Echo, path, remote string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	var healthErr error
	e := httpx.New(httpx.Options{Log: discard, Health: func(context.Context) error { return healthErr }})

	rec := get(e, "/healthz", "192.0.2.1:1234", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("healthy: %d %q", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("healthz may be cached")
	}
	head := httptest.NewRequest(http.MethodHead, "/healthz", nil)
	head.RemoteAddr = "192.0.2.1:1234"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, head)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", rec.Code)
	}

	healthErr = errors.New("sql: database is closed at /data/proxier.db")
	rec = get(e, "/healthz", "192.0.2.1:1234", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "database") || strings.Contains(rec.Body.String(), "/data") {
		t.Errorf("healthz reveals the error: %q", rec.Body)
	}
}

func TestClientIP(t *testing.T) {
	tunnel := netip.MustParsePrefix("172.18.0.5/32")
	for _, tc := range []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		realIP  string
		want    string
	}{
		{"no trusted proxies ignores the header", nil, "203.0.113.9:5000", "198.51.100.1", "203.0.113.9"},
		{"trusted proxy is believed", []netip.Prefix{tunnel}, "172.18.0.5:5000", "198.51.100.1", "198.51.100.1"},
		{"direct client is not believed", []netip.Prefix{tunnel}, "203.0.113.9:5000", "198.51.100.1", "203.0.113.9"},
		{"other private address is not believed", []netip.Prefix{tunnel}, "172.18.0.6:5000", "198.51.100.1", "172.18.0.6"},
		{"loopback is not believed", []netip.Prefix{tunnel}, "127.0.0.1:5000", "198.51.100.1", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := httpx.New(httpx.Options{Log: discard, TrustedProxies: tc.trusted})
			e.GET("/ip", func(c *echo.Context) error { return c.String(http.StatusOK, c.RealIP()) })
			rec := get(e, "/ip", tc.remote, map[string]string{"X-Real-IP": tc.realIP})
			if rec.Body.String() != tc.want {
				t.Errorf("RealIP = %q, want %q", rec.Body, tc.want)
			}
		})
	}
}

func TestClientIPMalformedHeader(t *testing.T) {
	e := httpx.New(httpx.Options{Log: discard, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}})
	e.GET("/ip", func(c *echo.Context) error { return c.String(http.StatusOK, c.RealIP()) })
	for header, want := range map[string]string{"": "172.18.0.5", "garbage": "172.18.0.5", "[2001:db8::1]": "2001:db8::1", "::ffff:198.51.100.1": "198.51.100.1"} {
		rec := get(e, "/ip", "172.18.0.5:5000", map[string]string{"X-Real-IP": header})
		if rec.Body.String() != want {
			t.Errorf("X-Real-IP %q: RealIP = %q, want %q", header, rec.Body, want)
		}
	}
}

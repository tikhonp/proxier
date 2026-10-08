// Package geoip looks up the country of an IP address: it asks a lookup URL
// the admin chose (default ipinfo.io) for the country of the IP. Servers use
// it to preselect a new server's location; subscriptions to name the country
// of the networks fetching a link. Callers send only the address they look
// up: a server's IP, or a network's first address, never a person's own.
package geoip

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Timeout bounds the lookup: the form must not wait for a slow service.
const Timeout = 3 * time.Second

// Client sends the request; tests use an httptest client.
var Client = &http.Client{Timeout: Timeout}

// Lookup returns the upper-case ISO country code for ip, or "" when the
// pattern is empty (the lookup is off), the IP is not an address, the request
// fails or the answer is not two letters. pattern holds {ip}.
func Lookup(ctx context.Context, pattern, ip string) string {
	pattern = strings.TrimSpace(pattern)
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if pattern == "" || err != nil || !strings.Contains(pattern, "{ip}") {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.ReplaceAll(pattern, "{ip}", url.PathEscape(addr.String())), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := Client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	code := strings.TrimSpace(string(body))
	if len(code) != 2 {
		return ""
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return ""
		}
	}
	return strings.ToUpper(code)
}

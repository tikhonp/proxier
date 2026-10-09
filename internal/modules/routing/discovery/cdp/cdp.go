// Package cdp is discovery's real browser: the Chromium sidecar, driven over
// the DevTools protocol with chromedp. Every visit is one fresh browser
// context (no cookies, no storage), disposed when it ends.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
)

// Browser is the Chromium at a DevTools address.
type Browser struct {
	host, port string
	client     *http.Client
}

// New returns the browser at raw ("http://chromium:9222"). Nothing is
// dialled until a visit or Version.
func New(raw string) (*Browser, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("cdp: %q is not a URL", raw)
	}
	port := u.Port()
	if port == "" {
		port = "9222"
	}
	return &Browser{host: u.Hostname(), port: port, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

// Peer is the address the SOCKS listener of a visit through a server
// routes to and accepts.
func (b *Browser) Peer() string { return net.JoinHostPort(b.host, b.port) }

type version struct {
	Browser   string `json:"Browser"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}

// endpoint resolves the host to an IP (DevTools refuses a Host header that
// is neither an IP nor localhost) and reads /json/version.
func (b *Browser) endpoint(ctx context.Context) (version, string, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", b.host)
	if err != nil || len(ips) == 0 {
		return version{}, "", fmt.Errorf("cdp: resolve %s: %w", b.host, err)
	}
	addr := net.JoinHostPort(ips[0].Unmap().String(), b.port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/json/version", nil)
	if err != nil {
		return version{}, "", err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return version{}, "", fmt.Errorf("cdp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return version{}, "", fmt.Errorf("cdp: /json/version answered %s", resp.Status)
	}
	var v version
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v); err != nil {
		return version{}, "", fmt.Errorf("cdp: /json/version: %w", err)
	}
	if v.WebSocket == "" {
		return version{}, "", errors.New("cdp: /json/version has no webSocketDebuggerUrl")
	}
	ws, err := url.Parse(v.WebSocket)
	if err != nil {
		return version{}, "", fmt.Errorf("cdp: webSocketDebuggerUrl: %w", err)
	}
	ws.Host = addr
	return v, ws.String(), nil
}

// Version is the browser's product ("HeadlessChrome/131.0.6778.85").
func (b *Browser) Version(ctx context.Context) (string, error) {
	v, _, err := b.endpoint(ctx)
	return v.Browser, err
}

// sameSite reports whether a link's host shares the registrable domain.
func sameSite(host, site string) bool {
	return host == site || strings.HasSuffix(host, "."+site)
}

var _ discovery.Browser = (*Browser)(nil)

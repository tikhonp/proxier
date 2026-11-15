// Package aicheck measures service exit IPs through endpoints and looks up
// their reputation without changing server health.
package aicheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/country"
)

const (
	Claude         = "claude"
	ChatGPT        = "chatgpt"
	OpenAIAPI      = "openai_api"
	ReputationMode = "ip"
	CacheAge       = 24 * time.Hour
	FreshAge       = 48 * time.Hour
)

// Reputation keeps absent flags unknown. Only the numeric score is required.
type Reputation struct {
	IP          string   `json:"ip"`
	Score       *float64 `json:"trust_score"`
	Country     string   `json:"countryCode"`
	Datacenter  *bool    `json:"is_datacenter"`
	Residential *bool    `json:"isResidential"`
	VPN         *bool    `json:"is_vpn"`
	Proxy       *bool    `json:"is_proxy"`
	Tor         *bool    `json:"is_tor"`
	Crawler     *bool    `json:"is_crawler"`
	Abuser      *bool    `json:"is_abuser"`
	Mobile      *bool    `json:"is_mobile"`
}

func publicIP(v string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(v)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, errors.New("invalid public IP")
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return netip.Addr{}, errors.New("invalid public IP")
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(ip) {
			return netip.Addr{}, errors.New("invalid public IP")
		}
	}
	return ip, nil
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001:2::/48"),
}

func readBody(resp *http.Response, limit int64) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("response unreadable or too large")
	}
	return b, nil
}

func observe(ctx context.Context, hc *http.Client, address string) (ip netip.Addr, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return ip, 0, errors.New("invalid trace address")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ip, 0, errors.New("trace request failed")
	}
	status = resp.StatusCode
	b, err := readBody(resp, 8192)
	if err != nil {
		return ip, status, err
	}
	if status != http.StatusOK {
		return ip, status, fmt.Errorf("trace HTTP %d", status)
	}
	var found bool
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "ip" {
			continue
		}
		if found {
			return netip.Addr{}, status, errors.New("duplicate trace IP")
		}
		found = true
		ip, err = publicIP(strings.TrimSpace(value))
		if err != nil {
			return netip.Addr{}, status, err
		}
	}
	if !found {
		return ip, status, errors.New("trace has no public IP")
	}
	return ip, status, nil
}

// Eligibility follows the country override on Net.Coffee's AI pages. Passing
// this list does not establish a provider's supported-country status.
// Source: ip.net.coffee/{claude,gpt}/*-page.js, checked 2026-11-15.
func Eligibility(code string) string {
	if !country.Valid(code) {
		return "unknown"
	}
	switch code {
	case "CN", "HK", "MO", "RU", "KP", "IR", "SY", "CU", "BY", "VE":
		return "restricted"
	default:
		return "not_restricted"
	}
}

func retryAfter(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at
	}
	return now.Add(time.Hour)
}

// lookup retries transient failures once. A rate limit instead sets a durable
// cooldown shared by all endpoints, without tying up a worker for Retry-After.
func lookup(ctx context.Context, hc *http.Client, base string, ip netip.Addr, now func() time.Time) (Reputation, time.Time, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return Reputation{}, time.Time{}, ctx.Err()
			case <-t.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/iprisk/"+ip.String(), nil)
		if err != nil {
			return Reputation{}, time.Time{}, errors.New("invalid reputation address")
		}
		resp, err := hc.Do(req)
		if err != nil {
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			return Reputation{}, time.Time{}, errors.New("reputation request failed")
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			_ = resp.Body.Close()
			return Reputation{}, retryAfter(resp.Header.Get("Retry-After"), now()), errors.New("reputation rate limited")
		}
		if resp.StatusCode >= 500 && attempt == 0 {
			_ = resp.Body.Close()
			continue
		}
		b, err := readBody(resp, 64<<10)
		if err != nil {
			return Reputation{}, time.Time{}, err
		}
		if resp.StatusCode != http.StatusOK {
			return Reputation{}, time.Time{}, fmt.Errorf("reputation HTTP %d", resp.StatusCode)
		}
		var r Reputation
		if err := json.NewDecoder(bytes.NewReader(b)).Decode(&r); err != nil || !json.Valid(b) || r.Score == nil || *r.Score < 0 || *r.Score > 100 {
			return Reputation{}, time.Time{}, errors.New("invalid reputation response")
		}
		returned, err := publicIP(r.IP)
		if err != nil || returned != ip {
			return Reputation{}, time.Time{}, errors.New("reputation IP mismatch")
		}
		r.IP, r.Country = ip.String(), strings.ToUpper(strings.TrimSpace(r.Country))
		if !country.Valid(r.Country) {
			r.Country = ""
		}
		return r, time.Time{}, nil
	}
	return Reputation{}, time.Time{}, errors.New("reputation request failed")
}

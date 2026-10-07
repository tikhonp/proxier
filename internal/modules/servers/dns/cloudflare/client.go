// Package cloudflare is the DNS driver for Cloudflare (docs/integrations/cloudflare.md):
// a small API client and the rules for which records Proxier may touch.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Cloudflare's API.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// ErrTokenRejected: Cloudflare answered 401 or 403. No retry fixes it.
var ErrTokenRejected = errors.New("cloudflare: token rejected or lacks DNS edit")

// ErrNotFound: Cloudflare answered 404 for a record.
var ErrNotFound = errors.New("cloudflare: not found")

// Zone is a zone the token can see.
type Zone struct{ ID, Name string }

// A is an A record.
type A struct {
	ID, Name, Content, Comment string
	TTL                        int
	Proxied                    bool
}

// Client talks to the API with one token.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Sleep waits between retries; tests replace it. It returns early with the
	// context's error.
	Sleep func(ctx context.Context, d time.Duration) error
	token string
}

// New returns a client for token: 15 s per request.
func New(token string) *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 15 * time.Second}, Sleep: sleep, token: token}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Retry limits (cloudflare.md): 429 waits what Cloudflare asks, up to five
// times; 5xx and network errors retry after 1, 3 and 9 seconds.
const maxRateLimited = 5

var serverBackoff = []time.Duration{time.Second, 3 * time.Second, 9 * time.Second}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// do sends one API call with the retry rules and returns the envelope of the
// answer. The token never appears in an error.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*envelope, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	rateLimited, serverErrors := 0, 0
	for {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		var raw []byte
		if err == nil {
			raw, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if serverErrors < len(serverBackoff) {
				if serr := c.Sleep(ctx, serverBackoff[serverErrors]); serr != nil {
					return nil, serr
				}
				serverErrors++
				continue
			}
			return nil, c.scrub(fmt.Errorf("cloudflare: %s %s: %w", method, path, err))
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			if rateLimited >= maxRateLimited {
				return nil, fmt.Errorf("cloudflare: rate limited %d times in a row", rateLimited+1)
			}
			rateLimited++
			if serr := c.Sleep(ctx, retryAfter(resp.Header.Get("Retry-After"))); serr != nil {
				return nil, serr
			}
			continue
		case resp.StatusCode >= 500:
			if serverErrors < len(serverBackoff) {
				if serr := c.Sleep(ctx, serverBackoff[serverErrors]); serr != nil {
					return nil, serr
				}
				serverErrors++
				continue
			}
			return nil, c.scrub(fmt.Errorf("cloudflare: %s %s: HTTP %d%s", method, path, resp.StatusCode, messageOf(raw)))
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, ErrTokenRejected
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		}

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("cloudflare: %s %s: HTTP %d, unreadable answer", method, path, resp.StatusCode)
		}
		if resp.StatusCode/100 != 2 || !env.Success {
			return nil, c.scrub(fmt.Errorf("cloudflare: %s %s: %s", method, path, firstMessage(env.Errors, resp.StatusCode)))
		}
		return &env, nil
	}
}

// retryAfter is how long Cloudflare asks to wait: its Retry-After seconds up
// to a minute, else ten seconds.
func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n >= 0 && n <= 60 {
		return time.Duration(n) * time.Second
	}
	return 10 * time.Second
}

func firstMessage(errs []apiError, status int) string {
	if len(errs) == 0 {
		return "HTTP " + strconv.Itoa(status)
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Message
		if e.Code != 0 {
			parts[i] += " (" + strconv.Itoa(e.Code) + ")"
		}
	}
	return strings.Join(parts, "; ")
}

func messageOf(raw []byte) string {
	var env envelope
	if json.Unmarshal(raw, &env) == nil && len(env.Errors) > 0 {
		return ": " + firstMessage(env.Errors, 0)
	}
	return ""
}

// scrub removes the token from an error, wherever it ended up.
func (c *Client) scrub(err error) error {
	if c.token == "" || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "•••"))
}

// VerifyToken asks Cloudflare whether the token is active.
func (c *Client) VerifyToken(ctx context.Context) error {
	env, err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, nil)
	if err != nil {
		return err
	}
	var r struct{ Status string }
	if err := json.Unmarshal(env.Result, &r); err != nil || r.Status != "active" {
		return ErrTokenRejected
	}
	return nil
}

// Zones lists every zone the token can see.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var out []Zone
	for page := 1; ; page++ {
		env, err := c.do(ctx, http.MethodGet, "/zones", url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			return nil, err
		}
		var zs []struct{ ID, Name string }
		if err := json.Unmarshal(env.Result, &zs); err != nil {
			return nil, fmt.Errorf("cloudflare: unreadable zone list: %w", err)
		}
		for _, z := range zs {
			out = append(out, Zone{ID: z.ID, Name: z.Name})
		}
		if page >= env.ResultInfo.TotalPages {
			return out, nil
		}
	}
}

type record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func (r record) a() A {
	return A{ID: r.ID, Name: r.Name, Content: r.Content, Comment: r.Comment, TTL: r.TTL, Proxied: r.Proxied}
}

// wire is the record Proxier writes: DNS-only (clients connect straight to
// nginx, and Let's Encrypt must reach port 80), TTL 60.
func wire(a A) record {
	return record{Type: "A", Name: a.Name, Content: a.Content, TTL: 60, Proxied: false, Comment: a.Comment}
}

// FindA lists the A records at name.
func (c *Client) FindA(ctx context.Context, zoneID, name string) ([]A, error) {
	env, err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records", url.Values{"type": {"A"}, "name": {name}}, nil)
	if err != nil {
		return nil, err
	}
	var rs []record
	if err := json.Unmarshal(env.Result, &rs); err != nil {
		return nil, fmt.Errorf("cloudflare: unreadable record list: %w", err)
	}
	out := make([]A, len(rs))
	for i, r := range rs {
		out[i] = r.a()
	}
	return out, nil
}

// GetA fetches a record by id; ErrNotFound when it is gone.
func (c *Client) GetA(ctx context.Context, zoneID, id string) (A, error) {
	env, err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records/"+id, nil, nil)
	if err != nil {
		return A{}, err
	}
	var r record
	if err := json.Unmarshal(env.Result, &r); err != nil {
		return A{}, fmt.Errorf("cloudflare: unreadable record: %w", err)
	}
	return r.a(), nil
}

// CreateA writes a new record; only the name, content and comment of a count.
func (c *Client) CreateA(ctx context.Context, zoneID string, a A) (A, error) {
	env, err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", nil, wire(a))
	if err != nil {
		return A{}, err
	}
	var r record
	if err := json.Unmarshal(env.Result, &r); err != nil {
		return A{}, fmt.Errorf("cloudflare: unreadable record: %w", err)
	}
	return r.a(), nil
}

// UpdateA replaces record id with a.
func (c *Client) UpdateA(ctx context.Context, zoneID, id string, a A) (A, error) {
	env, err := c.do(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+id, nil, wire(a))
	if err != nil {
		return A{}, err
	}
	var r record
	if err := json.Unmarshal(env.Result, &r); err != nil {
		return A{}, fmt.Errorf("cloudflare: unreadable record: %w", err)
	}
	return r.a(), nil
}

// DeleteRecord removes record id; ErrNotFound when it is already gone.
func (c *Client) DeleteRecord(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+id, nil, nil)
	return err
}

// Package checkhost is the client of check-host.net, the external checker
// (docs/integrations/check-host.md): which nodes exist, and whether a TCP
// port answers from a set of them. The response shapes were observed on the
// real API on 2026-10-08 (docs/integrations/check-host.md#api-as-observed).
package checkhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultBaseURL is the public service.
const DefaultBaseURL = "https://check-host.net"

// Node is a check-host node: its name is what a request names.
type Node struct {
	Name    string
	Country string // ISO alpha-2, lower case as the service writes it
	City    string
}

// NodeResult is one node's answer to a TCP check.
type NodeResult struct {
	Node      string
	OK        bool
	ConnectMS int
	// Error is the node's reason ("Connection timed out", "Connection
	// refused") or "no answer" when it had not answered when the wait ended.
	Error string
}

// Client talks to check-host.net. Zero values use the defaults: the public
// base URL, a 2 s poll and a 30 s wait.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Poll    time.Duration
	Wait    time.Duration
}

// ErrUnavailable wraps every failure to get an answer from the service.
var ErrUnavailable = errors.New("check-host is unavailable")

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (c *Client) poll() time.Duration {
	if c.Poll > 0 {
		return c.Poll
	}
	return 2 * time.Second
}

func (c *Client) wait() time.Duration {
	if c.Wait > 0 {
		return c.Wait
	}
	return 30 * time.Second
}

func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	u := c.base() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%w: unreadable answer: %v", ErrUnavailable, err)
	}
	return nil
}

// Nodes lists the nodes, sorted by name. The service answers
// {"nodes": {"ru1.node.check-host.net": {"asn", "ip", "location": [cc, country, city]}}}.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var resp struct {
		Nodes map[string]struct {
			Location []string `json:"location"`
		} `json:"nodes"`
	}
	if err := c.get(ctx, "/nodes/hosts", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(resp.Nodes))
	for name, n := range resp.Nodes {
		node := Node{Name: name}
		if len(n.Location) > 0 {
			node.Country = strings.ToLower(n.Location[0])
		}
		if len(n.Location) > 2 {
			node.City = n.Location[2]
		}
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CheckTCP asks the nodes to connect to hostPort and polls until each has
// answered or the wait is over. A node that has not answered by then gets a
// result with Error "no answer". The service answers a check with
// {"ok": 1, "request_id": "…"}; the result is {"<node>": null} while the node
// works and {"<node>": [{"address", "time"}]} (seconds) or
// {"<node>": [{"error": "Connection timed out"}]} afterwards.
func (c *Client) CheckTCP(ctx context.Context, hostPort string, nodes []string) ([]NodeResult, error) {
	q := url.Values{"host": {hostPort}, "node": nodes}
	var start struct {
		OK        int    `json:"ok"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
	}
	if err := c.get(ctx, "/check-tcp", q, &start); err != nil {
		return nil, err
	}
	if start.RequestID == "" || (start.OK != 1 && start.Error != "") {
		return nil, fmt.Errorf("%w: the check was refused: %s", ErrUnavailable, start.Error)
	}
	deadline := time.Now().Add(c.wait())
	var res map[string]json.RawMessage
	for {
		res = nil
		if err := c.get(ctx, "/check-result/"+url.PathEscape(start.RequestID), nil, &res); err != nil {
			return nil, err
		}
		pending := 0
		for _, n := range nodes {
			if raw, ok := res[n]; !ok || string(raw) == "null" {
				pending++
			}
		}
		if pending == 0 || !time.Now().Add(c.poll()).Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll()):
		}
	}
	out := make([]NodeResult, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeAnswer(n, res[n]))
	}
	return out, nil
}

// nodeAnswer reads one node's entry: connected when any entry has a time.
func nodeAnswer(node string, raw json.RawMessage) NodeResult {
	r := NodeResult{Node: node, Error: "no answer"}
	if len(raw) == 0 || string(raw) == "null" {
		return r
	}
	var entries []struct {
		Time  *float64 `json:"time"`
		Error string   `json:"error"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		r.Error = "unreadable answer"
		return r
	}
	for _, e := range entries {
		if e.Time != nil {
			return NodeResult{Node: node, OK: true, ConnectMS: int(math.Round(*e.Time * 1000))}
		}
	}
	if msg := entries[0].Error; msg != "" {
		r.Error = msg
	} else {
		r.Error = "no answer"
	}
	return r
}

// Package discoverytest is a fake discovery.Browser for tests: each URL
// answers a scripted Result, per visit path.
package discoverytest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"golang.org/x/net/proxy"
)

// Browser is a fake: each URL answers a scripted Result; Block holds visits
// until released; Fail makes the next visit fail like an unreachable Chromium.
type Browser struct {
	mu      sync.Mutex
	sites   map[string]discovery.Result // url + "|" + through
	visits  []discovery.Visit
	gate    chan struct{}
	fail    error
	probe   string
	probes  []string
	version string
	entered chan struct{}
}

// New returns a fake browser that knows no site: an unknown URL is a page
// that never loads (ERR_NAME_NOT_RESOLVED).
func New() *Browser {
	return &Browser{sites: map[string]discovery.Result{}, version: "HeadlessChrome/131.0.6778.85", entered: make(chan struct{}, 64)}
}

// Site scripts what a visit of url sees; through is "" for direct, or a
// server's name.
func (b *Browser) Site(url, through string, r discovery.Result) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sites[url+"|"+through] = r
}

// Block holds every visit until release is called.
func (b *Browser) Block() (release func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	gate := make(chan struct{})
	b.gate = gate
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if b.gate == gate {
				b.gate = nil
			}
			b.mu.Unlock()
			close(gate)
		})
	}
}

// Entered waits until a visit has started (and is perhaps held).
func (b *Browser) Entered(timeout time.Duration) bool {
	select {
	case <-b.entered:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Fail makes the next visit, and Version, fail with err (nil heals it).
func (b *Browser) Fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = err
}

// Probe makes every visit through a proxy fetch url through it; Probes
// lists what each fetch answered ("200 body" or the error).
func (b *Browser) Probe(url string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probe = url
}

// Probes lists the probe answers, in visit order.
func (b *Browser) Probes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.probes...)
}

// Visits lists the visits asked for, in order.
func (b *Browser) Visits() []discovery.Visit {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]discovery.Visit(nil), b.visits...)
}

// Visit answers the scripted result, capped at MaxHosts distinct hosts.
func (b *Browser) Visit(ctx context.Context, v discovery.Visit) (discovery.Result, error) {
	b.mu.Lock()
	b.visits = append(b.visits, v)
	gate, probe := b.gate, b.probe
	b.mu.Unlock()
	select {
	case b.entered <- struct{}{}:
	default:
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return discovery.Result{}, ctx.Err()
		}
	}
	b.mu.Lock()
	err := b.fail
	b.fail = nil
	b.mu.Unlock()
	if err != nil {
		return discovery.Result{}, err
	}
	if v.Proxy != "" && probe != "" {
		b.mu.Lock()
		b.probes = append(b.probes, fetch(ctx, v.Proxy, probe))
		b.mu.Unlock()
	}
	through := v.Path
	if v.Proxy == "" {
		through = ""
	}
	b.mu.Lock()
	r, ok := b.sites[v.URL+"|"+through]
	b.mu.Unlock()
	if !ok {
		return discovery.Result{Pages: []discovery.Page{{URL: v.URL, Error: "net::ERR_NAME_NOT_RESOLVED"}}}, nil
	}
	seen := map[string]bool{}
	var reqs []discovery.Request
	for _, q := range r.Requests {
		if !seen[q.Host] && len(seen) >= v.MaxHosts {
			r.Capped = true
			continue
		}
		seen[q.Host] = true
		reqs = append(reqs, q)
	}
	r.Requests = reqs
	return r, nil
}

// Version answers a fixed HeadlessChrome version.
func (b *Browser) Version(context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return "", b.fail
	}
	return b.version, nil
}

func fetch(ctx context.Context, proxyURL, target string) string {
	d, err := proxy.SOCKS5("tcp", strings.TrimPrefix(proxyURL, "socks5://"), nil, proxy.Direct)
	if err != nil {
		return err.Error()
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return "no context dialer"
	}
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: cd.DialContext}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err.Error()
	}
	resp, err := c.Do(req)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.Status[:3] + " " + string(body)
}

// ErrUnreachable is what an unreachable Chromium looks like.
var ErrUnreachable = errors.New("cdp: dial tcp 10.89.251.3:9222: connect: connection refused")

var _ discovery.Browser = (*Browser)(nil)

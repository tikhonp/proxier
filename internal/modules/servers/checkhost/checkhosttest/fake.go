// Package checkhosttest is a fake check-host.net that speaks the observed API.
package checkhosttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/checkhost"
)

// Answer is what a node says about a host.
type Answer struct {
	MS    int    // connect time when Error is empty
	Error string // "Connection timed out", "Connection refused"
	// Pending: the node never answers (still null when the wait ends).
	Pending bool
}

// Check is a recorded request.
type Check struct {
	HostPort string
	Nodes    []string
}

// Fake is the service.
type Fake struct {
	URL string

	mu      sync.Mutex
	nodes   []checkhost.Node
	answers map[string]Answer // "host|node"
	def     func(hostPort, node string) Answer
	polls   int // result polls that answer null before the nodes answer
	status  int
	checks  []Check
	reqs    map[string]*request
	nextID  int
}

type request struct {
	check Check
	polls int
}

// New starts the fake with a few Russian nodes and a few abroad.
func New(t testing.TB) *Fake {
	f := &Fake{answers: map[string]Answer{}, reqs: map[string]*request{}}
	f.nodes = []checkhost.Node{
		{Name: "ru1.node.check-host.net", Country: "ru", City: "Moscow"},
		{Name: "ru2.node.check-host.net", Country: "ru", City: "Moscow"},
		{Name: "ru3.node.check-host.net", Country: "ru", City: "Saint Petersburg"},
		{Name: "de1.node.check-host.net", Country: "de", City: "Nuremberg"},
		{Name: "de2.node.check-host.net", Country: "de", City: "Langen"},
		{Name: "nl1.node.check-host.net", Country: "nl", City: "Amsterdam"},
		{Name: "se1.node.check-host.net", Country: "se", City: "Stockholm"},
		{Name: "us1.node.check-host.net", Country: "us", City: "Los Angeles"},
	}
	f.def = func(string, string) Answer { return Answer{MS: 20} }
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Client is a client for the fake with fast polling.
func (f *Fake) Client() *checkhost.Client {
	return &checkhost.Client{BaseURL: f.URL, Poll: time.Millisecond, Wait: 60 * time.Millisecond}
}

// SetNodes replaces the node list.
func (f *Fake) SetNodes(n ...checkhost.Node) {
	f.mu.Lock()
	f.nodes = n
	f.mu.Unlock()
}

// Set makes node answer a for host.
func (f *Fake) Set(hostPort, node string, a Answer) {
	f.mu.Lock()
	f.answers[hostPort+"|"+node] = a
	f.mu.Unlock()
}

// SetAll makes every node's answer for host depend on its name: Russian nodes
// (ru*) get ru, the others abroad.
func (f *Fake) SetAll(ru, abroad Answer) {
	f.mu.Lock()
	f.def = func(_, node string) Answer {
		if strings.HasPrefix(node, "ru") {
			return ru
		}
		return abroad
	}
	f.mu.Unlock()
}

// PollsBeforeAnswer makes the first n result polls of a check answer null.
func (f *Fake) PollsBeforeAnswer(n int) {
	f.mu.Lock()
	f.polls = n
	f.mu.Unlock()
}

// Fail makes every request answer with this HTTP status; 0 lifts it.
func (f *Fake) Fail(status int) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

// Checks lists the TCP checks asked for, in order.
func (f *Fake) Checks() []Check {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Check(nil), f.checks...)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		http.Error(w, "unavailable", f.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/nodes/hosts":
		nodes := map[string]any{}
		for _, n := range f.nodes {
			nodes[n.Name] = map[string]any{"asn": "AS1", "ip": "192.0.2.1", "location": []string{n.Country, strings.ToUpper(n.Country), n.City}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
	case r.URL.Path == "/check-tcp":
		q := r.URL.Query()
		c := Check{HostPort: q.Get("host"), Nodes: q["node"]}
		f.checks = append(f.checks, c)
		f.nextID++
		id := fmt.Sprintf("req%d", f.nextID)
		f.reqs[id] = &request{check: c}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": 1, "request_id": id, "permanent_link": "https://check-host.net/check-report/" + id})
	case strings.HasPrefix(r.URL.Path, "/check-result/"):
		req := f.reqs[strings.TrimPrefix(r.URL.Path, "/check-result/")]
		if req == nil {
			http.NotFound(w, r)
			return
		}
		req.polls++
		out := map[string]any{}
		for _, n := range req.check.Nodes {
			a, ok := f.answers[req.check.HostPort+"|"+n]
			if !ok {
				a = f.def(req.check.HostPort, n)
			}
			switch {
			case a.Pending || req.polls <= f.polls:
				out[n] = nil
			case a.Error != "":
				out[n] = []map[string]any{{"error": a.Error}}
			default:
				out[n] = []map[string]any{{"address": "192.0.2.1", "time": float64(a.MS) / 1000}}
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

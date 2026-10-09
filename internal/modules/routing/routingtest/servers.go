package routingtest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"sync"

	"github.com/tikhonp/proxier/internal/modules/servers"
)

// Servers is an in-memory ServerHostnames (3b), EndpointCatalog and
// ProxyDialer (3g). Its dialer dials directly and records the server; it
// never starts xray.
type Servers struct {
	mu     sync.Mutex
	list   map[int64]fakeServer
	dialed []int64
	open   int
}

type fakeServer struct {
	name      string
	hostnames []string
	ip        netip.Addr
	health    string // "" healthy
}

// Put adds or replaces a server with its hostnames.
func (s *Servers) Put(id int64, name string, hostnames ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.list == nil {
		s.list = map[int64]fakeServer{}
	}
	s.list[id] = fakeServer{name: name, hostnames: hostnames, ip: netip.AddrFrom4([4]byte{203, 0, 113, byte(id)})}
}

// SetHealth changes a server's health ("blocked", "broken"…; "" healthy).
func (s *Servers) SetHealth(id int64, health string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.list[id]
	f.health = health
	s.list[id] = f
}

func (f fakeServer) healthWord() string {
	if f.health == "" {
		return "healthy"
	}
	return f.health
}

// Dialed lists the servers Dial was asked for, in order.
func (s *Servers) Dialed() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.dialed...)
}

// Open is how many dialers are open (not yet closed).
func (s *Servers) Open() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open
}

// Dial returns a dialer that connects directly, as if through the server.
func (s *Servers) Dial(_ context.Context, id int64) (servers.DialFunc, io.Closer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.list[id]; !ok {
		return nil, nil, fmt.Errorf("routingtest: no server %d", id)
	}
	s.dialed = append(s.dialed, id)
	s.open++
	var d net.Dialer
	return d.DialContext, closer{s}, nil
}

type closer struct{ s *Servers }

func (c closer) Close() error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.open--
	return nil
}

func (s *Servers) sorted() []int64 {
	ids := make([]int64, 0, len(s.list))
	for id := range s.list {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Servers lists every server with its hostnames, by id.
func (s *Servers) Servers(context.Context) ([]servers.ServerHostname, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []servers.ServerHostname
	for _, id := range s.sorted() {
		f := s.list[id]
		out = append(out, servers.ServerHostname{ServerID: id, Name: f.name, Hostnames: append([]string(nil), f.hostnames...), IP: f.ip.String()})
	}
	return out, nil
}

// Remove takes a server away (retired).
func (s *Servers) Remove(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.list, id)
}

// Active lists every server with its health, without endpoints.
func (s *Servers) Active(context.Context) ([]servers.ServerEndpoints, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []servers.ServerEndpoints
	for _, id := range s.sorted() {
		out = append(out, servers.ServerEndpoints{ServerID: id, Name: s.list[id].name, Health: s.list[id].healthWord()})
	}
	return out, nil
}

// Server returns one server.
func (s *Servers) Server(_ context.Context, id int64) (servers.ServerEndpoints, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.list[id]
	if !ok {
		return servers.ServerEndpoints{}, false, nil
	}
	return servers.ServerEndpoints{ServerID: id, Name: f.name, Health: f.healthWord()}, true, nil
}

var (
	_ servers.ServerHostnames = (*Servers)(nil)
	_ servers.EndpointCatalog = (*Servers)(nil)
	_ servers.ProxyDialer     = (*Servers)(nil)
)

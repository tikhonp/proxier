package routingtest

import (
	"context"
	"net/netip"
	"sort"
	"sync"

	"github.com/tikhonp/proxier/internal/modules/servers"
)

// Servers is an in-memory ServerHostnames (3b) and EndpointCatalog (3g); 3g
// adds the ProxyDialer.
type Servers struct {
	mu   sync.Mutex
	list map[int64]fakeServer
}

type fakeServer struct {
	name      string
	hostnames []string
	ip        netip.Addr
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

// Active lists every server as healthy, without endpoints.
func (s *Servers) Active(context.Context) ([]servers.ServerEndpoints, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []servers.ServerEndpoints
	for _, id := range s.sorted() {
		out = append(out, servers.ServerEndpoints{ServerID: id, Name: s.list[id].name, Health: "healthy"})
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
	return servers.ServerEndpoints{ServerID: id, Name: f.name, Health: "healthy"}, true, nil
}

var (
	_ servers.ServerHostnames = (*Servers)(nil)
	_ servers.EndpointCatalog = (*Servers)(nil)
)

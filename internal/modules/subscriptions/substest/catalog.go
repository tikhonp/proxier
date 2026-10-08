// Package substest runs the subscriptions module in tests: the full app on an
// in-memory EndpointCatalog (New), or with the real servers module
// (WithServers). Only tests import it.
package substest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
)

// Catalog is an in-memory servers.EndpointCatalog.
type Catalog struct {
	mu      sync.Mutex
	servers map[int64]servers.ServerEndpoints
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog { return &Catalog{servers: map[int64]servers.ServerEndpoints{}} }

func clone(s servers.ServerEndpoints) servers.ServerEndpoints {
	eps := make([]endpoint.Endpoint, len(s.Endpoints))
	for i, e := range s.Endpoints {
		p := map[string]string{}
		for k, v := range e.Params {
			p[k] = v
		}
		e.Params = p
		eps[i] = e
	}
	s.Endpoints = eps
	return s
}

// Put adds or replaces a server.
func (c *Catalog) Put(s servers.ServerEndpoints) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.servers[s.ServerID] = clone(s)
}

// Drop takes a server out of service.
func (c *Catalog) Drop(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.servers, id)
}

// SetHealth changes a server's health and its "since".
func (c *Catalog) SetHealth(id int64, health string, since time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.servers[id]
	s.Health, s.HealthSince = health, since
	c.servers[id] = s
}

// SetCredential gives every endpoint of the server a new credential, as a
// rotation does.
func (c *Catalog) SetCredential(id int64, credential string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.servers[id]
	for i := range s.Endpoints {
		s.Endpoints[i].Credential = credential
	}
	c.servers[id] = s
}

// Active lists the servers by id.
func (c *Catalog) Active(context.Context) ([]servers.ServerEndpoints, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]servers.ServerEndpoints, 0, len(c.servers))
	for _, s := range c.servers {
		out = append(out, clone(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerID < out[j].ServerID })
	return out, nil
}

// Server returns one server; ok is false when it isn't in the catalog.
func (c *Catalog) Server(_ context.Context, id int64) (servers.ServerEndpoints, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.servers[id]
	if !ok {
		return servers.ServerEndpoints{}, false, nil
	}
	return clone(s), true, nil
}

// HealthySince is when the servers of Server became healthy.
var HealthySince = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// Server is an active, healthy server with one vless-xhttp-tls endpoint "main":
// host "<name>.hosts.tikhonnnnn.com", port 443, a UUID derived from id, path
// "/<16 hex from id>", sni = host, mode stream-up, fp chrome, alpn h2,
// display name "<flag> <location> <number>".
func Server(id int64, name, flag, location string, number int) servers.ServerEndpoints {
	host := name + ".hosts.tikhonnnnn.com"
	return servers.ServerEndpoints{
		ServerID: id, Name: name, Flag: flag, Health: "healthy", HealthSince: HealthySince,
		Endpoints: []endpoint.Endpoint{{
			Key: "main", Type: endpoint.VlessXHTTPTLS, Host: host, Port: 443,
			Credential:  Credential(id),
			Params:      map[string]string{"path": Path(id), "sni": host, "mode": "stream-up", "fp": "chrome", "alpn": "h2"},
			DisplayName: endpoint.DisplayName(flag, location, number, "main", false),
		}},
	}
}

// Credential is the UUID Server gives server id.
func Credential(id int64) string { return fmt.Sprintf("6f1c2b7e-0d3a-4c8e-9a51-%012x", id) }

// Path is the secret path Server gives server id.
func Path(id int64) string { return fmt.Sprintf("/%016x", uint64(id)*0x9e3779b97f4a7c15) }

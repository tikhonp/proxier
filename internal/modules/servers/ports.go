package servers

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"sort"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/pages"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// The ports other modules use (docs/modules/servers.md#ports-for-other-modules).
// A module never reads this module's tables; it asks through these.

// ServerEndpoints is an active server with what a client needs to connect.
type ServerEndpoints struct {
	ServerID    int64
	Name        string
	Health      string
	HealthSince time.Time
	// Endpoints carry their credentials: callers must not log them.
	Endpoints []endpoint.Endpoint
}

// EndpointCatalog is what subscriptions read.
type EndpointCatalog interface {
	// Active lists the active servers that have endpoints, by id.
	Active(ctx context.Context) ([]ServerEndpoints, error)
	// Server returns one active server; ok is false for any other state.
	Server(ctx context.Context, id int64) (se ServerEndpoints, ok bool, err error)
}

// ServerHostnames is what routing reads.
type ServerHostnames interface {
	// Hostnames lists every management and proxy hostname and every IP of every
	// non-retired server.
	Hostnames(ctx context.Context) (names []string, ips []netip.Addr, err error)
}

// ProxyDialer sends traffic through a chosen active server's endpoint.
type ProxyDialer interface {
	Dial(ctx context.Context, serverID int64) (proxy.DialFunc, io.Closer, error)
}

// UsageReader names the subscriptions and links that serve a server. Phase 2
// implements it; until then the module shows "—".
type UsageReader = pages.UsageReader

// EndpointCatalog returns the module's catalog port.
func (m *Module) EndpointCatalog() EndpointCatalog { return catalog{m} }

// ServerHostnames returns the module's hostnames port.
func (m *Module) ServerHostnames() ServerHostnames { return hostnames{m} }

// ProxyDialer returns the module's dialer port.
func (m *Module) ProxyDialer() ProxyDialer { return dialer{m} }

// SetUsage gives the module the subscriptions port; nil (the default in
// Phase 1) makes its pages show "—".
func (m *Module) SetUsage(u UsageReader) { m.usage = u }

type catalog struct{ m *Module }

func (c catalog) Active(ctx context.Context) ([]ServerEndpoints, error) {
	all, err := store.ListServers(ctx, c.m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	var out []ServerEndpoints
	for _, s := range all {
		if s.State != "active" {
			continue
		}
		se, err := c.build(ctx, s)
		if err != nil {
			return nil, err
		}
		if len(se.Endpoints) > 0 {
			out = append(out, se)
		}
	}
	return out, nil
}

func (c catalog) Server(ctx context.Context, id int64) (ServerEndpoints, bool, error) {
	s, err := store.GetServer(ctx, c.m.deps.DB.R, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && s.State != "active" {
		return ServerEndpoints{}, false, nil
	}
	if err != nil {
		return ServerEndpoints{}, false, err
	}
	se, err := c.build(ctx, s)
	return se, err == nil, err
}

func (c catalog) build(ctx context.Context, s store.Server) (ServerEndpoints, error) {
	rows, err := store.Endpoints(ctx, c.m.deps.DB.R, s.ID)
	if err != nil {
		return ServerEndpoints{}, err
	}
	eps, err := sealed.OpenEndpoints(c.m.deps.Vault, rows)
	if err != nil {
		return ServerEndpoints{}, err
	}
	return ServerEndpoints{ServerID: s.ID, Name: s.Name, Health: s.Health, HealthSince: s.HealthSince.Time, Endpoints: eps}, nil
}

type hostnames struct{ m *Module }

func (h hostnames) Hostnames(ctx context.Context) ([]string, []netip.Addr, error) {
	all, err := store.ListServers(ctx, h.m.deps.DB.R)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	var names []string
	var ips []netip.Addr
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, s := range all {
		if s.State == "retired" {
			continue
		}
		add(s.ManagementHostname)
		add(s.ProxyHostname)
		if ip, err := netip.ParseAddr(s.IP); err == nil && !seen[s.IP] {
			seen[s.IP] = true
			ips = append(ips, ip)
		}
	}
	sort.Strings(names)
	sort.Slice(ips, func(i, j int) bool { return ips[i].Less(ips[j]) })
	return names, ips, nil
}

type dialer struct{ m *Module }

// Dial opens an xray client through the server's first endpoint. The closer
// stops it; the caller closes it when done.
func (d dialer) Dial(ctx context.Context, serverID int64) (proxy.DialFunc, io.Closer, error) {
	se, ok, err := catalog(d).Server(ctx, serverID)
	if err != nil {
		return nil, nil, err
	}
	if !ok || len(se.Endpoints) == 0 {
		return nil, nil, store.ErrNotFound
	}
	return proxy.Dialer(ctx, se.Endpoints[0], proxy.Options{})
}

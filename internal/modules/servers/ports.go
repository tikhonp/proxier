package servers

import (
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"sort"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/pages"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// The ports other modules use (docs/modules/servers.md#ports-for-other-modules).
// A module never reads this module's tables; it asks through these.

// ServerEndpoints is an active server with what a client needs to connect.
type ServerEndpoints struct {
	ServerID int64
	Name     string
	// Flag is the country flag of the server's location, for pages.
	Flag        string
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

// ServerHostname is a non-retired server with its hostnames, for routing's
// server-hostname guard.
type ServerHostname struct {
	ServerID  int64
	Name      string
	Hostnames []string // management and proxy, without empty or duplicate ones
	IP        string
}

// ServerHostnames is what routing reads.
type ServerHostnames interface {
	// Servers lists every server whose state isn't retired (a retiring one
	// included: its DNS still points at it), by id.
	Servers(ctx context.Context) ([]ServerHostname, error)
}

// RoutedName is a hostname a routing list covers: the list's domain and the
// service that holds it.
type RoutedName = provision.RoutedName

// RoutingGuard is what provisioning asks routing (set by SetRouting): which
// of a new server's hostnames a routing list covers. Nil: nothing is checked.
type RoutingGuard = provision.RoutingGuard

// DialFunc opens a TCP connection through a server's endpoint. It is
// declared here so another module needs no other package of this one.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ProxyDialer sends traffic through a chosen active server's endpoint.
type ProxyDialer interface {
	Dial(ctx context.Context, serverID int64) (DialFunc, io.Closer, error)
}

// Rotator is what subscriptions' cut-off uses to rotate servers one at a time.
type Rotator interface {
	// RotationRequest is the job that rotates the server, for the caller to
	// enqueue in its own transaction. ErrNotActive: not active, retiring or
	// gone. ErrNothingToRotate: its version marks no generated value rotate: true.
	RotationRequest(ctx context.Context, serverID int64, actor string) (jobs.Request, error)
}

// The errors of Rotator.
var (
	ErrNotActive       = deploy.ErrNotActive
	ErrNothingToRotate = deploy.ErrNothingToRotate
)

// UsageReader names the subscriptions and links that serve a server. Phase 2
// implements it; until then the module shows "—".
type UsageReader = pages.UsageReader

// EndpointCatalog returns the module's catalog port.
func (m *Module) EndpointCatalog() EndpointCatalog { return catalog{m} }

// ServerHostnames returns the module's hostnames port.
func (m *Module) ServerHostnames() ServerHostnames { return hostnames{m} }

// ProxyDialer returns the module's dialer port.
func (m *Module) ProxyDialer() ProxyDialer { return dialer{m} }

// Rotator returns the module's rotation port.
func (m *Module) Rotator() Rotator { return rotator{m} }

// SetRouting gives the module routing's guard; nil (no routing module)
// makes provisioning check nothing.
func (m *Module) SetRouting(g RoutingGuard) { m.routing = g }

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
		if s.State != "active" || s.Retiring() {
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
	if errors.Is(err, store.ErrNotFound) || err == nil && (s.State != "active" || s.Retiring()) {
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
	return ServerEndpoints{ServerID: s.ID, Name: s.Name, Flag: country.Flag(s.Country), Health: s.Health, HealthSince: s.HealthSince.Time, Endpoints: eps}, nil
}

type rotator struct{ m *Module }

func (r rotator) RotationRequest(ctx context.Context, serverID int64, actor string) (jobs.Request, error) {
	req, err := r.m.Deploy.RotationRequest(ctx, serverID, actor)
	if errors.Is(err, store.ErrNotFound) {
		return req, ErrNotActive
	}
	return req, err
}

type hostnames struct{ m *Module }

func (h hostnames) Servers(ctx context.Context) ([]ServerHostname, error) {
	all, err := store.ListServers(ctx, h.m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	var out []ServerHostname
	for _, s := range all {
		if s.State == "retired" {
			continue
		}
		sh := ServerHostname{ServerID: s.ID, Name: s.Name, IP: s.IP}
		for _, n := range []string{s.ManagementHostname, s.ProxyHostname} {
			if n != "" && !slices.Contains(sh.Hostnames, n) {
				sh.Hostnames = append(sh.Hostnames, n)
			}
		}
		out = append(out, sh)
	}
	return out, nil
}

type dialer struct{ m *Module }

// Dial opens an xray client through the server's first endpoint. The closer
// stops it; the caller closes it when done.
func (d dialer) Dial(ctx context.Context, serverID int64) (DialFunc, io.Closer, error) {
	se, ok, err := catalog(d).Server(ctx, serverID)
	if err != nil {
		return nil, nil, err
	}
	if !ok || len(se.Endpoints) == 0 {
		return nil, nil, store.ErrNotFound
	}
	dial, closer, err := proxy.Dialer(ctx, se.Endpoints[0], proxy.Options{})
	if err != nil {
		return nil, nil, err
	}
	return DialFunc(dial), closer, nil
}

package routing

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// The port router scripts use (docs/modules/routing.md#ports-for-other-modules).
// A module never reads this module's tables; it asks through it.

// FieldErrors are the port's field errors (field → i18n key), so a consumer
// reads them with errors.As without importing a store.
type FieldErrors = store.FieldErrors

// ErrRouterNotFound: no such router (a removed one has no row).
var ErrRouterNotFound = errors.New("routing: no such router")

// RegistrarBy is the created_by of a router a router script registered, and
// router_added's "by".
const RegistrarBy = "routerscripts"

// The names a registered router gets (fresh-router.rsc's): what a router
// script's @fill routing-address-list and routing-doh-forwarder parameters
// are filled with for a new router.
var (
	DefaultAddressList = routeros.Defaults.List
	DefaultForwarder   = routeros.Defaults.Forwarder
)

// RegistrarList is a routing list a router can follow.
type RegistrarList struct {
	ID      int64
	Name    string
	Default bool
}

// RegisteredRouter is a router as it is now.
type RegisteredRouter struct {
	ID            int64
	Name, List    string
	State         string // awaiting, active, paused, removing
	AwaitingUntil time.Time
	ConnectedAt   time.Time
	LastSyncAt    time.Time
	LastResult    string // "", synced, failed
	LastError     string
	Host, Jump    string // "10.230.3.1:22", "dacha-pi:22" ("" without one)
	AddressList   string
	Forwarder     string
	KeyCommands   string // the three commands that install Proxier's key for its user
}

// JumpHost is a jump host some router goes through.
type JumpHost struct {
	Host    string
	Port    int
	User    string
	Tailnet bool
	Pinned  bool // Proxier has its key
}

// RouterRegistration is the router a router script will set up.
type RouterRegistration struct {
	Name     string
	ListID   int64 // 0: the default list
	Host     string
	Port     int    // 0: 22
	User     string // "": proxier
	JumpHost string
	JumpPort int
	JumpUser string
	Tailnet  bool
}

// RouterRegistrar is how router scripts register the router they set up.
type RouterRegistrar interface {
	// Lists are the routing lists, the default first.
	Lists(ctx context.Context) ([]RegistrarList, error)
	// Routers are every router that isn't being removed, by name.
	Routers(ctx context.Context) ([]RegisteredRouter, error)
	// Router reads one router; ErrRouterNotFound once removed.
	Router(ctx context.Context, id int64) (RegisteredRouter, error)
	// JumpHosts are the distinct jump hosts of the routers.
	JumpHosts(ctx context.Context) ([]JumpHost, error)
	// Register saves the router in awaiting setup inside tx; FieldErrors
	// (as Add router's) before anything is written.
	Register(ctx context.Context, tx *sqlx.Tx, r RouterRegistration, actor string) (RegisteredRouter, error)
}

// RouterRegistrar returns the module's router port. It reads the services
// when called, so main.go hands it over before Init.
func (m *Module) RouterRegistrar() RouterRegistrar { return registrar{m} }

type registrar struct{ m *Module }

func (g registrar) Lists(ctx context.Context) ([]RegistrarList, error) {
	rows, err := store.Lists(ctx, g.m.deps.DB.R)
	if err != nil {
		return nil, err
	}
	out := make([]RegistrarList, 0, len(rows))
	for _, l := range rows {
		out = append(out, RegistrarList{ID: l.ID, Name: l.Name, Default: l.IsDefault})
	}
	return out, nil
}

// key is Proxier's public key line ("" when it can't be read: the commands
// then show an empty key rather than fail the page).
func (g registrar) key(ctx context.Context) string {
	line, _, err := g.m.deps.SSH.PublicKey(ctx)
	if err != nil {
		return ""
	}
	return line
}

func (g registrar) router(r routers.Router, key string) RegisteredRouter {
	return RegisteredRouter{
		ID: r.ID, Name: r.Name, List: r.List, State: r.State, AwaitingUntil: r.AwaitingUntil, ConnectedAt: r.ConnectedAt,
		LastSyncAt: r.LastSyncAt, LastResult: r.LastResult, LastError: r.LastError,
		Host: r.Conn.Address(), Jump: r.Conn.JumpAddress(), AddressList: r.Conn.Names.List, Forwarder: r.Conn.Names.Forwarder,
		KeyCommands: routeros.KeyCommands(r.Conn.User, key),
	}
}

func (g registrar) Routers(ctx context.Context) ([]RegisteredRouter, error) {
	rows, err := g.m.Routers.List(ctx)
	if err != nil {
		return nil, err
	}
	key := g.key(ctx)
	out := make([]RegisteredRouter, 0, len(rows))
	for _, r := range rows {
		if r.State != routers.StateRemoving {
			out = append(out, g.router(r, key))
		}
	}
	return out, nil
}

func (g registrar) Router(ctx context.Context, id int64) (RegisteredRouter, error) {
	r, err := g.m.Routers.Get(ctx, id)
	if errors.Is(err, routers.ErrNotFound) {
		return RegisteredRouter{}, ErrRouterNotFound
	}
	if err != nil {
		return RegisteredRouter{}, err
	}
	return g.router(r, g.key(ctx)), nil
}

func (g registrar) JumpHosts(ctx context.Context) ([]JumpHost, error) {
	rows, err := g.m.Routers.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[JumpHost]bool{}
	var out []JumpHost
	for _, r := range rows {
		c := r.Conn
		if c.JumpHost == "" {
			continue
		}
		j := JumpHost{Host: c.JumpHost, Port: c.JumpPort, User: c.JumpUser, Tailnet: c.Tailnet}
		if seen[j] {
			continue
		}
		seen[j] = true
		_, err := g.m.deps.SSH.KnownHost(ctx, c.JumpAddress())
		switch {
		case err == nil:
			j.Pinned = true
		case !errors.Is(err, sshx.ErrNotFound):
			return nil, err
		}
		out = append(out, j)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Host != out[b].Host {
			return out[a].Host < out[b].Host
		}
		return out[a].Port < out[b].Port
	})
	return out, nil
}

func (g registrar) Register(ctx context.Context, tx *sqlx.Tx, r RouterRegistration, actor string) (RegisteredRouter, error) {
	reg, err := g.m.Routers.RegisterTx(ctx, tx, routers.Registration{Name: r.Name, ListID: r.ListID, Conn: routers.Connection{
		Host: r.Host, Port: r.Port, User: r.User, JumpHost: r.JumpHost, JumpPort: r.JumpPort, JumpUser: r.JumpUser, Tailnet: r.Tailnet,
	}}, RegistrarBy, actor)
	if err != nil {
		return RegisteredRouter{}, err
	}
	rt, err := g.m.Routers.GetIn(ctx, tx, reg.ID)
	if err != nil {
		return RegisteredRouter{}, err
	}
	return g.router(rt, g.key(ctx)), nil
}

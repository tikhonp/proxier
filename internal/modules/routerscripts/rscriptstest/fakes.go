package rscriptstest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
)

// FakeLinks is subscriptions' LinkIssuer in memory: subscriptions "Family"
// (id 1, 3 servers) and "Me" (id 2, 4 servers). Issue keeps the link (it
// can't follow the caller's rollback: tests that need that use WithModules)
// with the URL https://proxier.test/s/<name hash>.
type FakeLinks struct {
	mu    sync.Mutex
	subs  []subscriptions.IssuerSubscription
	links []subscriptions.IssuedLink
}

func newFakeLinks() *FakeLinks {
	return &FakeLinks{subs: []subscriptions.IssuerSubscription{{ID: 1, Name: "Family", Servers: 3}, {ID: 2, Name: "Me", Servers: 4}}}
}

func fakeURL(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "https://proxier.test/s/" + hex.EncodeToString(sum[:8])
}

func (f *FakeLinks) Subscriptions(context.Context) ([]subscriptions.IssuerSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]subscriptions.IssuerSubscription(nil), f.subs...), nil
}

func (f *FakeLinks) Links(context.Context) ([]subscriptions.IssuedLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []subscriptions.IssuedLink
	for _, l := range f.links {
		if l.State != "deleted" {
			l.URL = ""
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *FakeLinks) Link(_ context.Context, id int64) (subscriptions.IssuedLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.ID == id {
			if l.State == "deleted" {
				l.URL = ""
			}
			return l, nil
		}
	}
	return subscriptions.IssuedLink{}, subscriptions.ErrLinkNotFound
}

func (f *FakeLinks) Issue(_ context.Context, _ *sqlx.Tx, name string, subscriptionID int64, _ string) (subscriptions.IssuedLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name = strings.TrimSpace(name)
	fe := subscriptions.FieldErrors{}
	for _, l := range f.links {
		if l.Name == name && l.State != "deleted" {
			fe["name"] = "links.err.name_taken"
		}
	}
	sub := ""
	for _, s := range f.subs {
		if s.ID == subscriptionID {
			sub = s.Name
		}
	}
	if sub == "" {
		fe["subscription"] = "links.err.subscription"
	}
	if len(fe) > 0 {
		return subscriptions.IssuedLink{}, fe
	}
	l := subscriptions.IssuedLink{ID: int64(len(f.links) + 1), Name: name, Subscription: sub, State: "active", URL: fakeURL(name)}
	f.links = append(f.links, l)
	return l, nil
}

// Add puts a link in, as if made on the Links page; its id.
func (f *FakeLinks) Add(name, subscription, state string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := subscriptions.IssuedLink{ID: int64(len(f.links) + 1), Name: name, Subscription: subscription, State: state, URL: fakeURL(name)}
	f.links = append(f.links, l)
	return l.ID
}

// Regenerate gives a link a new URL; Delete deletes it.
func (f *FakeLinks) Regenerate(id int64) {
	f.set(id, func(l *subscriptions.IssuedLink) { l.URL = fakeURL(l.URL) })
}
func (f *FakeLinks) Delete(id int64) {
	f.set(id, func(l *subscriptions.IssuedLink) { l.State = "deleted" })
}

func (f *FakeLinks) set(id int64, fn func(*subscriptions.IssuedLink)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.links {
		if f.links[i].ID == id {
			fn(&f.links[i])
		}
	}
}

// FakeRouters is routing's RouterRegistrar in memory: list "Main" (id 1,
// the default) and "Kids" (id 2). Register keeps the router, awaiting setup,
// with the default names; SetState moves it.
type FakeRouters struct {
	mu      sync.Mutex
	routers []routing.RegisteredRouter
	// Jumps are the known jump hosts.
	Jumps []routing.JumpHost
	// Now is the clock of AwaitingUntil.
	Now func() time.Time
}

func newFakeRouters(now func() time.Time) *FakeRouters { return &FakeRouters{Now: now} }

var fakeLists = []routing.RegistrarList{{ID: 1, Name: "Main", Default: true}, {ID: 2, Name: "Kids"}}

func (f *FakeRouters) Lists(context.Context) ([]routing.RegistrarList, error) {
	return append([]routing.RegistrarList(nil), fakeLists...), nil
}

func (f *FakeRouters) Routers(context.Context) ([]routing.RegisteredRouter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []routing.RegisteredRouter
	for _, r := range f.routers {
		if r.State != "removing" && r.State != "removed" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *FakeRouters) Router(_ context.Context, id int64) (routing.RegisteredRouter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.routers {
		if r.ID == id && r.State != "removed" {
			return r, nil
		}
	}
	return routing.RegisteredRouter{}, routing.ErrRouterNotFound
}

func (f *FakeRouters) JumpHosts(context.Context) ([]routing.JumpHost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]routing.JumpHost(nil), f.Jumps...), nil
}

func (f *FakeRouters) Register(_ context.Context, _ *sqlx.Tx, r routing.RouterRegistration, _ string) (routing.RegisteredRouter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fe := routing.FieldErrors{}
	name := strings.TrimSpace(r.Name)
	for _, x := range f.routers {
		if x.Name == name && x.State != "removed" {
			fe["name"] = "routers.err.name_taken"
		}
	}
	if r.Host == "" || strings.ContainsAny(r.Host, "/: ") {
		fe["host"] = "routers.err.host"
	}
	list := ""
	for _, l := range fakeLists {
		if l.ID == r.ListID || r.ListID == 0 && l.Default {
			list = l.Name
		}
	}
	if list == "" {
		fe["list"] = "routers.err.list"
	}
	if len(fe) > 0 {
		return routing.RegisteredRouter{}, fe
	}
	port := r.Port
	if port == 0 {
		port = 22
	}
	rt := routing.RegisteredRouter{
		ID: int64(len(f.routers) + 1), Name: name, List: list, State: "awaiting", AwaitingUntil: f.Now().Add(7 * 24 * time.Hour),
		Host: r.Host + ":" + strconv.Itoa(port), AddressList: routing.DefaultAddressList, Forwarder: routing.DefaultForwarder,
		KeyCommands: FakeKeyCommands,
	}
	if r.JumpHost != "" {
		rt.Jump = r.JumpHost + ":" + strconv.Itoa(r.JumpPort)
	}
	f.routers = append(f.routers, rt)
	return rt, nil
}

// Add puts a router in, as if added on the Routers page; its id.
func (f *FakeRouters) Add(name, state, addressList, forwarder string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	rt := routing.RegisteredRouter{ID: int64(len(f.routers) + 1), Name: name, List: "Main", State: state, AddressList: addressList, Forwarder: forwarder}
	f.routers = append(f.routers, rt)
	return rt.ID
}

// FakeKeyCommands are what every fake router gives as its key commands.
const FakeKeyCommands = "/user ssh-keys add user=proxier key=\"ssh-ed25519 AAAAfake proxier\""

// Update changes a router in place (its sync, its deadline).
func (f *FakeRouters) Update(id int64, fn func(*routing.RegisteredRouter)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.routers {
		if f.routers[i].ID == id {
			fn(&f.routers[i])
		}
	}
}

// SetState moves a router ("removed" makes it gone).
func (f *FakeRouters) SetState(id int64, state string, connected time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.routers {
		if f.routers[i].ID == id {
			f.routers[i].State, f.routers[i].ConnectedAt = state, connected
		}
	}
}

package routingtest

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"golang.org/x/crypto/ssh"
)

// RouterOption changes Router.
type RouterOption func(*routerOpts)

type routerOpts struct{ jump bool }

// ViaJump puts a fake jump host in front of the router.
func ViaJump() RouterOption { return func(o *routerOpts) { o.jump = true } }

// Key is Proxier's public key, which the fakes authorize.
func (h *Harness) Key() ssh.PublicKey {
	h.T.Helper()
	line, _, err := h.App.SSH.PublicKey(context.Background())
	if err != nil {
		h.T.Fatal(err)
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		h.T.Fatal(err)
	}
	return k
}

// Conn is the connection of a fake router (through jump when not nil).
func Conn(r *routerostest.Router, jump *sshxtest.Server) routers.Connection {
	host, port := splitAddr(r.Addr)
	c := routers.Connection{Host: host, Port: port, User: routers.DefaultUser}
	if jump != nil {
		c.JumpHost, c.JumpPort = splitAddr(jump.Addr)
		c.JumpUser = "pi"
	}
	return c.Normalize()
}

// Router starts a fake router, adds it as an active, connected router
// following the list (Main when 0) with its host keys pinned, and returns
// both. Nothing is queued: a test syncs it with SyncNow or a change.
func (h *Harness) Router(name string, listID int64, opts ...RouterOption) (*routerostest.Router, int64) {
	h.T.Helper()
	var o routerOpts
	for _, opt := range opts {
		opt(&o)
	}
	ctx := context.Background()
	key := h.Key()
	r := routerostest.New(h.T, key)
	var jump *sshxtest.Server
	if o.jump {
		jump = routerostest.Jump(h.T, key)
	}
	if listID == 0 {
		l, err := h.Mod.Lists.Default(ctx)
		if err != nil {
			h.T.Fatal(err)
		}
		listID = l.ID
	}
	c := Conn(r, jump)
	id, err := h.Mod.Routers.Create(ctx, name, listID, c, 0, "admin")
	if err != nil {
		h.T.Fatalf("create router %s: %v", name, err)
	}
	if err := h.App.SSH.Pin(ctx, c.Address(), routers.RouterSubject(id), r.Server.HostKey(), "admin"); err != nil {
		h.T.Fatal(err)
	}
	if jump != nil {
		if err := h.App.SSH.Pin(ctx, c.JumpAddress(), routers.JumpSubject(c.JumpAddress()), jump.HostKey(), "admin"); err != nil {
			h.T.Fatal(err)
		}
	}
	h.Exec(`UPDATE routing_routers SET connected_at = ?, last_seen_at = ?, version = '7.24.5 (stable)', board = 'RB5009UG+S+' WHERE id = ?`,
		db.At(h.Clock()), db.At(h.Clock()), id)
	h.mu.Lock()
	if h.fakes == nil {
		h.fakes = map[int64]*routerostest.Router{}
	}
	h.fakes[id] = r
	h.mu.Unlock()
	return r, id
}

// RouterFake is the fake of a router Router added.
func (h *Harness) RouterFake(id int64) *routerostest.Router {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakes[id]
}

// Clock reads the module's clock.
func (h *Harness) Clock() time.Time { return h.Mod.Now() }

// JobsOf lists the jobs of a type, oldest first.
func (h *Harness) JobsOf(typ string) []jobs.Job {
	h.T.Helper()
	list, err := h.App.Jobs.List(context.Background(), jobs.Filter{Type: typ, Limit: 1000})
	if err != nil {
		h.T.Fatal(err)
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}

// WaitFor polls cond (real time, up to 30 s) while jobs run.
func (h *Harness) WaitFor(what string, cond func() bool) {
	h.T.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.T.Fatalf("timed out waiting for %s", what)
}

func splitAddr(addr string) (string, int) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 22
	}
	n, _ := strconv.Atoi(port)
	return host, n
}

// Settle waits until no job runs and none is due on the module's clock:
// delayed syncs and retries waiting for their backoff stay queued until the
// test moves the clock past them.
func (h *Harness) Settle() {
	h.T.Helper()
	h.App.Jobs.Kick()
	deadline := time.Now().Add(60 * time.Second)
	calm := 0
	for time.Now().Before(deadline) {
		var n int
		if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE state IN ('running', 'interrupted')
			OR (state = 'queued' AND run_after <= ?)`, db.At(h.Clock())); err != nil {
			h.T.Fatal(err)
		}
		if n == 0 {
			if calm++; calm >= 3 {
				return
			}
		} else {
			calm = 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.T.Fatal("jobs did not settle")
}

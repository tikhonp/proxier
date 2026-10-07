// Package tailnet is Proxier's own node on the headscale tailnet (ADR 0008):
// an embedded tsnet client used only to dial out to jump hosts and routers.
// It serves nothing on the tailnet.
package tailnet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tikhonp/proxier/internal/platform/config"
	"tailscale.com/tsnet"
)

// ErrOff is returned by Dial when there is no node.
var ErrOff = errors.New("tailnet: off (no PROXIER_TS_AUTHKEY)")

// State is where the node is.
type State string

const (
	Off        State = "off"
	Starting   State = "starting"
	Running    State = "running"
	NeedsLogin State = "needs_login"
	Error      State = "error"
)

// Status is what Settings shows.
type Status struct {
	State      State
	Name       string // the node's name on the tailnet
	IP         netip.Addr
	KeyExpiry  time.Time // zero: does not expire, or unknown
	Err        string
	ControlURL string
}

// upTimeout is how long the node gets to reach Running before it is reported
// as needing a login (a pre-auth key that expired or was used up).
const upTimeout = 2 * time.Minute

// Node is the tailnet node. The zero value is not usable; call New.
type Node struct {
	cfg *config.Config
	log *slog.Logger

	mu     sync.Mutex
	srv    *tsnet.Server
	state  State
	err    string
	cancel context.CancelFunc
	done   chan struct{} // closed when srv's up goroutine has ended
}

// New returns a node that is off until Start.
func New(cfg *config.Config, log *slog.Logger) *Node {
	return &Node{cfg: cfg, log: log, state: Off}
}

func (n *Node) dir() string { return filepath.Join(n.cfg.DataDir, "tailnet") }

// hasState reports whether a node was joined before: its state file is kept.
func (n *Node) hasState() bool {
	_, err := os.Stat(filepath.Join(n.dir(), "tailscaled.state"))
	return err == nil
}

// Start joins the tailnet in the background and returns at once; HTTP does
// not wait for it. Without PROXIER_TS_AUTHKEY the node stays off, unless it
// joined earlier (re-authenticated from Settings): its state is then reused.
func (n *Node) Start(ctx context.Context) {
	if !n.cfg.TailnetEnabled() && !n.hasState() {
		return
	}
	n.begin(ctx, n.cfg.TSAuthKey)
}

// begin starts a server with authKey; the key is only used when the node's
// state does not already hold a login.
func (n *Node) begin(ctx context.Context, authKey string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.startLocked(ctx, authKey)
}

func (n *Node) startLocked(ctx context.Context, authKey string) {
	n.state, n.err = Starting, ""
	srv := &tsnet.Server{
		Dir: n.dir(), Hostname: n.cfg.TSHostname, ControlURL: n.cfg.TSControlURL, AuthKey: authKey, Ephemeral: false,
		Logf:     func(format string, args ...any) { n.log.Debug(fmt.Sprintf(format, args...), "from", "tsnet") },
		UserLogf: func(format string, args ...any) { n.log.Info(fmt.Sprintf(format, args...), "from", "tsnet") },
	}
	n.srv = srv
	upCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	n.cancel = cancel
	n.done = make(chan struct{})
	go n.up(upCtx, srv, n.done)
}

func (n *Node) up(ctx context.Context, srv *tsnet.Server, done chan struct{}) {
	defer close(done)
	if err := os.MkdirAll(n.dir(), 0o700); err != nil {
		n.fail(srv, err, Error)
		return
	}
	upCtx, cancel := context.WithTimeout(ctx, upTimeout)
	defer cancel()
	st, err := srv.Up(upCtx)
	switch {
	case err == nil:
		n.mu.Lock()
		if n.srv == srv {
			n.state = Running
		}
		n.mu.Unlock()
		n.log.Info("tailnet: running", "name", n.cfg.TSHostname, "ip", firstIP(st.TailscaleIPs))
	case ctx.Err() != nil:
		// Replaced or closed: whoever did it owns the state.
	case errors.Is(err, context.DeadlineExceeded):
		n.fail(srv, errors.New("did not reach the tailnet in time: the pre-auth key may be expired or used up"), NeedsLogin)
	default:
		n.fail(srv, err, Error)
	}
}

func firstIP(ips []netip.Addr) string {
	if len(ips) == 0 {
		return ""
	}
	return ips[0].String()
}

func (n *Node) fail(srv *tsnet.Server, err error, state State) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.srv != srv {
		return
	}
	n.state, n.err = state, err.Error()
	n.log.Error("tailnet", "error", err, "state", state)
}

// Dial opens a connection over the tailnet.
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	srv := n.srv
	n.mu.Unlock()
	if srv == nil {
		return nil, ErrOff
	}
	return srv.Dial(ctx, network, addr)
}

// Status reports the node.
func (n *Node) Status(ctx context.Context) Status {
	n.mu.Lock()
	srv, state, errText := n.srv, n.state, n.err
	n.mu.Unlock()
	st := Status{State: state, Err: errText, Name: n.cfg.TSHostname, ControlURL: n.cfg.TSControlURL}
	if srv == nil || state == Off {
		st.State = Off
		return st
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s, err := lc.StatusWithoutPeers(ctx)
	if err != nil || s == nil {
		return st
	}
	if s.Self != nil {
		if s.Self.KeyExpiry != nil {
			st.KeyExpiry = *s.Self.KeyExpiry
		}
		if s.Self.HostName != "" {
			st.Name = s.Self.HostName
		}
	}
	if len(s.TailscaleIPs) > 0 {
		st.IP = s.TailscaleIPs[0]
	}
	if s.BackendState == "NeedsLogin" && st.State != Starting {
		st.State = NeedsLogin
	}
	return st
}

// Reauthenticate closes the node and starts it again with a new pre-auth key.
// The key is never stored: it is used for this login only.
func (n *Node) Reauthenticate(ctx context.Context, authKey string) error {
	if authKey == "" {
		return errors.New("tailnet: no pre-auth key given")
	}
	err := n.stop()
	n.begin(ctx, authKey)
	return err
}

// stop ends the node. tsnet's Close must not run while Start does, so the
// start goroutine is cancelled and awaited first, without holding the lock it
// needs to report.
func (n *Node) stop() error {
	n.mu.Lock()
	srv, cancel, done := n.srv, n.cancel, n.done
	n.srv, n.cancel, n.done = nil, nil, nil
	n.state, n.err = Off, ""
	n.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if srv != nil {
		return srv.Close()
	}
	return nil
}

// Close stops the node.
func (n *Node) Close() error { return n.stop() }

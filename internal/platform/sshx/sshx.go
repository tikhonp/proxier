// Package sshx is Proxier's SSH client (ADR 0007): one ed25519 identity,
// host keys pinned per address, an optional jump host, commands with timeouts
// streaming into a job's log, and SFTP uploads. Nothing is installed on the
// far side.
package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
	"golang.org/x/crypto/ssh"
)

// Network says how the first hop is reached.
type Network int

const (
	// Direct dials from the container's own network.
	Direct Network = iota
	// Tailnet dials through Proxier's tailnet node.
	Tailnet
)

// FirstContact says what happens when an address has no pinned key yet.
type FirstContact int

const (
	// PinOnFirstContact pins the key once the handshake has succeeded: servers
	// at provisioning, where nobody could have confirmed a fingerprint.
	PinOnFirstContact FirstContact = iota
	// ConfirmFirstContact refuses with *UnknownHostError, so the admin sees
	// the fingerprint and calls Pin: routers and jump hosts.
	ConfirmFirstContact
)

// Hop is one SSH endpoint.
type Hop struct {
	Address string // "host:port"; the port defaults to 22
	User    string
	// Subject names who the host belongs to: "server:12", "router:3",
	// "jump:parents-pi". ForgetSubject uses it.
	Subject string
}

// Target is where a connection ends up.
type Target struct {
	Hop
	// Jump is dialled first, and Hop is reached through it.
	Jump *Hop
	// Network applies to the first hop.
	Network      Network
	FirstContact FirstContact
}

// HostKeyChangedError is returned while an address's key differs from the
// pinned one, until the admin accepts or forgets it.
type HostKeyChangedError struct {
	Address string
	// Old and New are SHA256 fingerprints.
	Old, New string
	// TypeChanged: the host no longer has a key of the pinned type.
	TypeChanged bool
}

func (e *HostKeyChangedError) Error() string {
	what := "host key changed"
	if e.TypeChanged {
		what = "host key type changed"
	}
	return fmt.Sprintf("sshx: %s for %s (pinned %s, now %s): accept the new key in Settings → SSH", what, e.Address, e.Old, e.New)
}

// UnknownHostError is returned for an address with no pinned key under
// ConfirmFirstContact. Nothing is pinned; call Pin with Key to trust it.
type UnknownHostError struct {
	Address, Fingerprint string
	Key                  ssh.PublicKey
}

func (e *UnknownHostError) Error() string {
	return fmt.Sprintf("sshx: first contact with %s (%s): confirm the fingerprint to trust it", e.Address, e.Fingerprint)
}

var (
	// ErrTimeout is returned by Run when the command ran over its timeout; the
	// exit code is unknown.
	ErrTimeout = errors.New("sshx: command timed out")
	// ErrAuth is returned when the host refused Proxier's key.
	ErrAuth = errors.New("sshx: the host refused Proxier's key")
	// ErrNoIdentity is returned before EnsureIdentity has run.
	ErrNoIdentity = errors.New("sshx: no SSH identity yet")
	// ErrNotFound is returned for an unknown known-host id.
	ErrNotFound = errors.New("sshx: no such known host")
	// ErrNoPending is returned by Accept when the host has no new key waiting.
	ErrNoPending = errors.New("sshx: the host has no new key to accept")
	// ErrHostKnown is returned by Pin when the address is pinned to another key.
	ErrHostKnown = errors.New("sshx: the address is already pinned to another key")
)

// Dialer opens a connection, like net.Dialer.DialContext. The tailnet node's
// Dial is one.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// SSH is the platform's SSH client.
type SSH struct {
	// Now is the clock; tests replace it.
	Now func() time.Time
	// DialTimeout and HandshakeTimeout bound a connection (15 s each).
	DialTimeout, HandshakeTimeout time.Duration
	// KeepAlive is how often an idle connection is probed; three misses close it.
	KeepAlive time.Duration

	d       *db.DB
	v       *vault.Vault
	ev      *events.Catalog
	st      *settings.Store
	tailnet Dialer
	log     *slog.Logger
}

// New builds the client. tailnet may be nil: a Tailnet target then fails as
// if the tailnet were off.
func New(d *db.DB, v *vault.Vault, ev *events.Catalog, st *settings.Store, tailnet Dialer, log *slog.Logger) *SSH {
	return &SSH{
		Now: time.Now, DialTimeout: 15 * time.Second, HandshakeTimeout: 15 * time.Second, KeepAlive: 30 * time.Second,
		d: d, v: v, ev: ev, st: st, tailnet: tailnet, log: log,
	}
}

func (s *SSH) now() db.Time { return db.At(s.Now()) }

// Events are the event types the SSH client records.
var Events = []events.Type{
	{Name: "ssh.key_generated", Module: "platform", Notify: true, Emoji: "🔐",
		Description: "Proxier's SSH key was regenerated: every server, jump host and router needs the new one.",
		NotifyIf:    func(p map[string]any) bool { b, _ := p["regenerated"].(bool); return b }},
	{Name: "ssh.host_key_pinned", Module: "platform", Description: "An SSH host key was pinned on first contact."},
	{Name: "ssh.host_key_changed", Module: "platform", Notify: true, Emoji: "🔐",
		Description: "A pinned SSH host key changed; work with that host stops."},
	{Name: "ssh.host_key_accepted", Module: "platform", Description: "A new SSH host key was accepted."},
	{Name: "ssh.host_forgotten", Module: "platform", Description: "A pinned SSH host key was forgotten."},
}

// Subjects of the SSH events: the identity, and a known host by its address.
var (
	identitySubject = events.Subject{Type: "ssh", ID: "identity"}
)

func hostSubject(address string) events.Subject { return events.Subject{Type: "ssh_host", ID: address} }

// SubjectTypes are the subject types the SSH events use.
var SubjectTypes = []string{"ssh", "ssh_host"}

// capWriter keeps the first Max bytes written and drops the rest.
type capWriter struct {
	buf []byte
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

var _ io.Writer = (*capWriter)(nil)

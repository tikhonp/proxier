// Package ipchange moves an active server to another IP of the same VPS and
// keeps its previous IPs (docs/processes/servers/ip-change.md): the new IP
// must answer SSH with the server's own host key and pass the proxy test from
// home before DNS is repointed. Clients follow the hostname, so links and
// connection URIs stay as they are.
package ipchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// JobType is the IP change.
const JobType = "servers.change_ip"

// Step names; titles are job.servers.change_ip.step.<name>.
const (
	StepConnect   = "connect"
	StepProxyTest = "proxy-test"
	StepDNS       = "dns"
	StepWaitDNS   = "wait-dns"
)

// Errors a caller can tell apart.
var (
	ErrNotActive = errors.New("ipchange: the server is not active")
	ErrNotFound  = errors.New("ipchange: no such previous IP")
)

// maxError is how much of an error an event keeps.
const maxError = 2000

// FieldErrors and Msg are provisioning's: a refusal names the form field and
// an i18n key, and nothing was changed.
type (
	FieldErrors = provision.FieldErrors
	Msg         = provision.Msg
)

// Seams are what tests replace, provisioning's (the module reads them at use).
type Seams struct {
	ProxyTest    func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Result
	ProxyOptions func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Options
	Attempts     int
	Gap          time.Duration
}

// Health is what the change needs of the checks.
type Health interface {
	// ResetForIPChange makes the health unknown in tx, the IP just stored.
	ResetForIPChange(ctx context.Context, tx *sqlx.Tx, id int64, actor string) error
	// QueueAfterIPChange queues the checks of the new IP in tx, the change
	// job marked succeeded.
	QueueAfterIPChange(ctx context.Context, tx *sqlx.Tx, id int64, actor string) error
}

// Deps is what the service needs.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Vault    *vault.Vault
	Jobs     *jobs.System
	SSH      *sshx.SSH
	Settings *settings.Store
	Log      *slog.Logger
	Health   Health
	// ProxyTestURL is the template's proxy test object of the server ("" =
	// the setting); StackNamesIP whether its stack would change at ip.
	ProxyTestURL func(ctx context.Context, serverID int64) (string, error)
	StackNamesIP func(ctx context.Context, serverID int64, ip string) (bool, error)
	// Read at use: tests replace the module's driver, waiter and seams.
	DNS    func() dns.Driver
	Waiter func() dns.Waiter
	Seams  func() Seams
}

// Service changes servers' IPs and forgets previous ones.
type Service struct {
	Deps
	Now func() time.Time
}

// New returns the service.
func New(d Deps) *Service { return &Service{Deps: d, Now: time.Now} }

func (s *Service) now() time.Time { return s.Now() }

// Form is the Change IP dialog.
type Form struct {
	IP string
	// Keep keeps the IP the server leaves as a previous IP, tested from home.
	Keep bool
	// Force switches even when the new IP fails the proxy test from home.
	Force bool
}

// payload is the job's.
type payload struct {
	ServerID int64  `json:"server_id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Keep     bool   `json:"keep"`
	Force    bool   `json:"force"`
}

func serverKey(id int64) string { return "server:" + strconv.FormatInt(id, 10) }

// Check validates a form for a server: the IP it would move to, or why not.
func (s *Service) Check(ctx context.Context, srv store.Server, f Form) (string, error) {
	if srv.State != "active" || srv.Retiring() {
		return "", ErrNotActive
	}
	ip := strings.TrimSpace(f.IP)
	addr, err := netip.ParseAddr(ip)
	switch {
	case ip == "":
		return "", FieldErrors{"ip": {Key: "servers.err.ip_required"}}
	case err != nil || !addr.Is4() || addr.Is4In6():
		return "", FieldErrors{"ip": {Key: "servers.err.ip_v4"}}
	}
	ip = addr.String()
	if ip == srv.IP {
		return "", FieldErrors{"ip": {Key: "servers.ip.err.same", Args: map[string]any{"name": srv.Name}}}
	}
	// The target may be an additional address of this same server; a previous
	// IP of it is fine (switch back).
	if name, taken, err := store.AddressHolder(ctx, s.DB.R, ip); err != nil {
		return "", err
	} else if taken && name == srv.Name {
		return "", FieldErrors{"ip": {Key: "servers.ip.err.additional", Args: map[string]any{"name": srv.Name}}}
	}
	other, ok, err := store.HolderOfIP(ctx, s.DB.R, ip)
	switch {
	case err != nil:
		return "", err
	case ok && other.ServerID != srv.ID && other.Previous:
		return "", FieldErrors{"ip": {Key: "servers.err.ip_previous", Args: map[string]any{"name": other.Name}}}
	case ok && other.ServerID != srv.ID:
		return "", FieldErrors{"ip": {Key: "servers.err.ip_used", Args: map[string]any{"name": other.Name}}}
	}
	return ip, nil
}

// Change queues the move of a server to the form's IP and returns the job.
// The server stays at its IP, untouched, until the new one has passed.
func (s *Service) Change(ctx context.Context, serverID int64, f Form, actor string) (int64, error) {
	var id int64
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		srv, err := store.GetServer(ctx, tx, serverID)
		if err != nil {
			return err
		}
		ip, err := s.Check(ctx, srv, f)
		if err != nil {
			return err
		}
		e, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
			Type: JobType, ResourceKey: serverKey(srv.ID), CreatedBy: actor,
			Payload: payload{ServerID: srv.ID, From: srv.IP, To: ip, Keep: f.Keep, Force: f.Force},
		})
		id = e.ID
		return err
	})
	if err == nil {
		s.Jobs.Kick()
	}
	return id, err
}

// JobType is the job to register.
func (s *Service) JobType() jobs.Type {
	return jobs.Type{
		Name: JobType, Queue: jobs.Provisioning, MaxAttempts: 1, // a failure is the member's to retry
		Steps: []jobs.Step{
			step(StepConnect, s.connect),
			step(StepProxyTest, s.proxyTest),
			step(StepDNS, s.dnsStep),
			step(StepWaitDNS, s.waitDNS),
		},
		OnSucceeded: s.onSucceeded,
		OnFailed:    s.onFailed,
		OnCancelled: s.onCancelled,
	}
}

// step runs fn with a context that ends when the member cancels.
func step(name string, fn func(ctx context.Context, r *jobs.Run) error) jobs.Step {
	return jobs.Step{Name: name, Run: func(ctx context.Context, r *jobs.Run) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-r.Cancelling():
				cancel()
			case <-ctx.Done():
			}
		}()
		err := fn(ctx, r)
		if err != nil {
			select {
			case <-r.Cancelling():
				return jobs.ErrCancelled
			default:
			}
		}
		return err
	}}
}

// load reads the payload and the server, which must still be active and at
// the IP the change started from (or, once the DNS step stored it, the new one).
func (s *Service) load(ctx context.Context, r *jobs.Run) (payload, store.Server, error) {
	var p payload
	if err := r.Payload(&p); err != nil {
		return p, store.Server{}, err
	}
	srv, err := store.GetServer(ctx, s.DB.R, p.ServerID)
	if err != nil {
		return p, srv, err
	}
	if srv.State != "active" || srv.Retiring() {
		return p, srv, jobs.Permanent(errors.New("the server is not active any more"))
	}
	if srv.IP != p.From && srv.IP != p.To {
		return p, srv, jobs.Permanent(fmt.Errorf("%s moved to %s meanwhile", srv.Name, srv.IP))
	}
	return p, srv, nil
}

// connect proves SSH access before moving the IP. A separate SSH connection IP
// stays unchanged; otherwise the new IP must present the old IP's host key.
func (s *Service) connect(ctx context.Context, r *jobs.Run) error {
	p, srv, err := s.load(ctx, r)
	if err != nil {
		return err
	}
	port := strconv.Itoa(srv.SSHPort)
	if srv.SSHIP != "" {
		c, err := s.SSH.Connect(ctx, sshx.Target{
			Hop:          sshx.Hop{Address: net.JoinHostPort(srv.ConnectIP(), port), User: srv.LoginUser(), Subject: serverKey(srv.ID)},
			FirstContact: sshx.PinOnFirstContact,
		}, r.Log())
		if err != nil {
			return fmt.Errorf("no SSH to %s at its SSH connection IP %s: %w", srv.Name, srv.ConnectIP(), err)
		}
		_ = c.Close()
		r.Log().Info("SSH access through %s is unchanged; the new IP %s is checked by the proxy test", srv.ConnectIP(), p.To)
		return nil
	}
	c, err := s.SSH.Connect(ctx, sshx.Target{
		Hop: sshx.Hop{Address: net.JoinHostPort(p.To, port), User: srv.LoginUser(), Subject: serverKey(srv.ID)},
		// Pin the new address only when it shows the old one's key.
		FirstContact: sshx.PinOnFirstContact, SameKeyAs: net.JoinHostPort(p.From, port),
	}, r.Log())
	var other *sshx.OtherHostError
	switch {
	case errors.As(err, &other):
		return jobs.Permanent(fmt.Errorf("%s answers with another host key (%s) than %s (%s): it is another machine; nothing was changed",
			p.To, other.Got, srv.Name, other.Want))
	case err != nil:
		return fmt.Errorf("no SSH to %s at %s: %w; is the IP set up on the VPS?", srv.Name, net.JoinHostPort(p.To, port), err)
	}
	_ = c.Close()
	r.Log().Info("%s answers at %s with its own host key", srv.Name, p.To)
	return nil
}

// proxyTest runs the proxy test from home through every primary-address
// endpoint at the new IP, as clients will once DNS leads there, with provisioning's pace.
func (s *Service) proxyTest(ctx context.Context, r *jobs.Run) error {
	p, srv, err := s.load(ctx, r)
	if err != nil {
		return err
	}
	rows, err := store.Endpoints(ctx, s.DB.R, srv.ID)
	if err != nil {
		return err
	}
	eps, err := sealed.OpenEndpoints(s.Vault, rows)
	if err != nil {
		return err
	}
	addrs, err := store.ServerAddresses(ctx, s.DB.R, srv.ID)
	if err != nil {
		return err
	}
	tplURL, err := s.ProxyTestURL(ctx, srv.ID)
	if err != nil {
		return err
	}
	url, timeout, stall, err := conf.ProxyTest(ctx, s.Settings, tplURL)
	if err != nil {
		return err
	}
	sm := s.seams()
	for _, e := range eps {
		// An endpoint on an additional address stays at that address, which
		// the change does not touch: testing it says nothing about the new IP
		// (and aimed at the new IP it would fail for the wrong reason).
		if onAdditionalAddress(addrs, e.Host) {
			r.Log().Info("Skipped %s: it is on an additional address, which this change leaves as it is", e.Key)
			continue
		}
		opts := proxy.Options{URL: url, Timeout: timeout, Stall: stall, Resolve: map[string]string{
			net.JoinHostPort(e.Host, strconv.Itoa(e.Port)): net.JoinHostPort(p.To, strconv.Itoa(e.Port)),
		}}
		if sm.ProxyOptions != nil {
			opts = sm.ProxyOptions(ctx, e, opts)
		}
		var last proxy.Result
		for attempt := 1; attempt <= sm.Attempts; attempt++ {
			r.Log().Info("Proxy test through %s at %s, attempt %d of %d", e.Key, p.To, attempt, sm.Attempts)
			if last = sm.ProxyTest(ctx, e, opts); last.OK {
				r.Log().Info("Proxy test passed: first byte after %d ms, %d kbit/s", last.FirstByteMS, last.ThroughputKbps)
				break
			}
			r.Log().Warn("Proxy test failed (%s): %s", last.Class, last.Error)
			if attempt < sm.Attempts {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(sm.Gap):
				}
			}
		}
		if last.OK {
			continue
		}
		if !p.Force {
			return jobs.Permanent(fmt.Errorf("the proxy test through %s at %s failed from home too (%s: %s); nothing was changed",
				e.Key, p.To, last.Class, last.Error))
		}
		r.Log().Warn("Switching anyway, as asked")
	}
	return nil
}

func (s *Service) seams() Seams {
	var sm Seams
	if s.Seams != nil {
		sm = s.Seams()
	}
	if sm.ProxyTest == nil {
		sm.ProxyTest = proxy.Test
	}
	if sm.Attempts < 1 {
		sm.Attempts = 3
	}
	if sm.Gap <= 0 {
		sm.Gap = 20 * time.Second
	}
	return sm
}

// onAdditionalAddress tells whether an endpoint's host is the hostname of one
// of the server's additional addresses. Endpoints store no address number; an
// address-bound endpoint takes that address's hostname as its host.
func onAdditionalAddress(addrs []store.Address, host string) bool {
	for _, a := range addrs {
		if a.Hostname == host {
			return true
		}
	}
	return false
}

// hostnames are the names whose records lead to the server.
func hostnames(srv store.Server) []string {
	names := []string{srv.ManagementHostname}
	if srv.ProxyHostname != srv.ManagementHostname {
		names = append(names, srv.ProxyHostname)
	}
	return names
}

// dnsStep points the server's records at the new IP and, in one transaction,
// stores the IP, keeps or forgets the old one and records the change. From
// here the server is at the new IP; a retry of the step changes nothing more.
func (s *Service) dnsStep(ctx context.Context, r *jobs.Run) error {
	p, srv, err := s.load(ctx, r)
	if err != nil {
		return err
	}
	if srv.IP == p.To {
		r.Log().Info("%s is at %s already", srv.Name, p.To)
		return nil
	}
	// Proxier writes the records in its zones; a record made by hand the
	// member points first, and the change checks it.
	toWrite := hostnames(srv)
	if srv.ManualDNS {
		toWrite = nil
		w := s.Waiter()
		for _, name := range hostnames(srv) {
			found, err := w.Check(ctx, name, p.To)
			if err != nil {
				return fmt.Errorf("check the record of %s: %w", name, err)
			}
			if !found.OK {
				return jobs.Permanent(fmt.Errorf("%s is in none of the Cloudflare zones, so its record is made by hand, and it does not point to %s yet (%s). Change it to A %s → %s at your DNS provider, then change the IP again; nothing was changed", name, p.To, found.Said, name, p.To))
			}
			r.Log().Info("%s points to %s (%s)", name, p.To, found.Said)
		}
	}
	drv := s.DNS()
	var recs []dns.Record
	for _, name := range toWrite {
		rec, err := drv.Ensure(ctx, name, p.To, srv.Name, false)
		var conflict *dns.ConflictError
		if errors.As(err, &conflict) {
			return jobs.Permanent(fmt.Errorf("%s points to %s in a record Proxier did not create; nothing was changed", conflict.Name, conflict.Content))
		}
		if err != nil {
			return err
		}
		recs = append(recs, rec)
		r.Log().Info("DNS record %s → %s is in place", name, p.To)
	}
	actor := r.Info().Actor()
	at := db.At(s.now())
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, rec := range recs {
			if err := store.UpsertDNSRecord(ctx, tx, store.DNSRecord{
				ServerID: srv.ID, Provider: rec.Provider, ZoneID: rec.ZoneID, Zone: rec.Zone, Name: rec.Name, Type: rec.Type,
				Content: rec.Content, RecordID: rec.RecordID,
			}, at); err != nil {
				return err
			}
		}
		// The new IP may be a previous one: switching back.
		back, err := store.DeletePreviousIP(ctx, tx, srv.ID, p.To)
		if err != nil {
			return err
		}
		if err := store.SetServerIP(ctx, tx, srv.ID, p.To, at); err != nil {
			return err
		}
		if p.Keep {
			if err := store.AddPreviousIP(ctx, tx, srv.ID, p.From, at); err != nil {
				return err
			}
		}
		if _, err := s.Events.Record(ctx, tx, events.Event{
			Type: "server.ip_changed", Subject: store.ServerSubject(srv.ID), Actor: actor,
			Payload: map[string]any{"from": p.From, "to": p.To, "kept": p.Keep, "back": back, "forced": p.Force},
		}); err != nil {
			return err
		}
		return s.Health.ResetForIPChange(ctx, tx, srv.ID, actor)
	})
	if err != nil {
		return err
	}
	if !p.Keep {
		s.forgetPin(ctx, srv, p.From, actor)
	}
	r.Log().Info("%s is at %s now", srv.Name, p.To)
	if p.Keep {
		r.Log().Info("%s stays as a previous IP, tested from home", p.From)
	}
	if names, err := s.StackNamesIP(ctx, srv.ID, p.From); err != nil {
		r.Log().Warn("Could not tell whether the stack names the IP: %v", err)
	} else if names {
		r.Log().Warn("The stack's files name the IP: redeploy %s to bring them to %s", srv.Name, p.To)
	}
	return nil
}

// forgetPin drops the host key pinned at an IP the server no longer has, so
// another server built there later is a first contact, not a changed key.
func (s *Service) forgetPin(ctx context.Context, srv store.Server, ip, actor string) {
	if srv.SSHIP != "" && ip == srv.SSHIP {
		return // still the SSH connection IP, though no longer the server's IP
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(srv.SSHPort))
	h, err := s.SSH.KnownHost(ctx, addr)
	if errors.Is(err, sshx.ErrNotFound) {
		return
	}
	if err == nil {
		err = s.SSH.Forget(ctx, h.ID, actor)
	}
	if err != nil {
		s.Log.Warn("servers: forget the host key of a former IP", "server", srv.Name, "address", addr, "error", err)
	}
}

// waitDNS waits for the public resolvers to show the new IP. The change is
// made either way: one that is slow to show only delays the clients, so it
// is a warning, not a failure.
func (s *Service) waitDNS(ctx context.Context, r *jobs.Run) error {
	_, srv, err := s.load(ctx, r)
	if err != nil {
		return err
	}
	recs, err := store.DNSRecords(ctx, s.DB.R, srv.ID)
	if err != nil {
		return err
	}
	zones := map[string]string{}
	for _, rec := range recs {
		zones[rec.Name] = rec.Zone
	}
	w := s.Waiter()
	for _, name := range hostnames(srv) {
		if srv.ManualDNS {
			zones[name] = w.Apex(ctx, name)
		}
		r.Log().Info("Waiting for resolvers to show %s → %s", name, srv.IP)
		err := w.Wait(ctx, zones[name], name, srv.IP, r.Log())
		switch {
		case errors.Is(err, dns.ErrNotVisible):
			r.Log().Warn("%s is not visible on every resolver yet; clients follow once it is", name)
		case err != nil:
			return err
		}
	}
	return nil
}

// onSucceeded queues the checks of the new IP as the job ends: the self-check
// takes the server's key, which the job no longer holds.
func (s *Service) onSucceeded(ctx context.Context, tx *sqlx.Tx, j jobs.Info) error {
	var p payload
	var raw string
	if err := tx.GetContext(ctx, &raw, `SELECT payload FROM jobs WHERE id = ?`, j.ID); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	if err := s.Health.QueueAfterIPChange(ctx, tx, p.ServerID, j.Actor()); err != nil {
		return err
	}
	s.Jobs.Kick()
	return s.Jobs.LogLine(ctx, tx, j.ID, "info", "Checks of the new IP are queued")
}

// --- failure

func (s *Service) onFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	return s.stopped(ctx, tx, j, cause.Error(), false)
}

func (s *Service) onCancelled(ctx context.Context, tx *sqlx.Tx, j jobs.Info, _ string) error {
	return s.stopped(ctx, tx, j, "cancelled", true)
}

// stopped records server.ip_change_failed. Before the DNS step the server is
// untouched at its IP; after it, at the new one.
func (s *Service) stopped(ctx context.Context, tx *sqlx.Tx, j jobs.Info, errText string, cancelled bool) error {
	var row struct {
		Payload string `db:"payload"`
		Step    string `db:"error_step"`
	}
	if err := tx.GetContext(ctx, &row, `SELECT payload, COALESCE(error_step, '') AS error_step FROM jobs WHERE id = ?`, j.ID); err != nil {
		return err
	}
	var p payload
	if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
		return err
	}
	if cancelled {
		_ = tx.GetContext(ctx, &row.Step, `SELECT name FROM job_steps WHERE job_id = ? AND state = 'cancelled' ORDER BY idx LIMIT 1`, j.ID)
	}
	if len(errText) > maxError {
		errText = errText[:maxError]
	}
	pl := map[string]any{"to": p.To, "step": row.Step, "error": errText}
	if cancelled {
		pl["cancelled"] = true
	}
	_, err := s.Events.Record(ctx, tx, events.Event{Type: "server.ip_change_failed", Subject: store.ServerSubject(p.ServerID), Actor: j.Actor(), Payload: pl})
	return err
}

// --- previous IPs

// Forget stops testing a previous IP of a server and drops its results and
// its pinned host key.
func (s *Service) Forget(ctx context.Context, serverID, previousID int64, actor string) error {
	var srv store.Server
	var ip string
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		if srv, err = store.GetServer(ctx, tx, serverID); err != nil {
			return err
		}
		if srv.State == "retired" || srv.Retiring() {
			return ErrNotActive
		}
		prev, err := store.GetPreviousIP(ctx, tx, serverID, previousID)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		ip = prev.IP
		if _, err := store.DeletePreviousIP(ctx, tx, serverID, prev.IP); err != nil {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{
			Type: "server.previous_ip_forgotten", Subject: store.ServerSubject(serverID), Actor: actor,
			Payload: map[string]any{"ip": prev.IP},
		})
		return err
	})
	if err != nil {
		return err
	}
	s.forgetPin(ctx, srv, ip, actor)
	return nil
}

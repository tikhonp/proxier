// Package retire takes a server out of service for good
// (docs/processes/servers/server-retirement.md): it stops being offered at
// once, a job cancels what still works on it, removes the stack when chosen
// and reachable, deletes the DNS records Proxier made, erases the generated
// values and forgets the host key.
package retire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// JobType is the retirement job.
const JobType = "servers.retire"

// Step names, in order.
const (
	StepCancel      = "cancel-provisioning"
	StepRemoveStack = "remove-stack"
	StepDNS         = "dns"
	StepFinish      = "finish"
)

// provisionJob is the type of the job that builds a server; retirement is the
// one place besides the server page that cancels it.
const provisionJob = "servers.provision"

// maxFailedError is how much of an error the event keeps.
const maxFailedError = 2000

// Errors a caller can tell apart.
var (
	ErrNotAllowed   = errors.New("retire: the server is retired or already retiring")
	ErrNameMismatch = errors.New("retire: the typed name does not match")
	ErrNotRetiring  = errors.New("retire: the server's retirement has not failed")
)

// UsageReader names the subscriptions and links that serve a server (Phase 2
// implements it; the dialog says so while nothing does).
type UsageReader interface {
	Usage(ctx context.Context, serverID int64) (subscriptions []string, links int, err error)
}

// Setup is what the stack removal needs of the server's template version.
type Setup struct {
	Server    store.Server
	Dir       string
	Uninstall []manifest.Step
}

// Deps is what the service needs of the platform and the module.
type Deps struct {
	DB     *db.DB
	Events *events.Catalog
	Jobs   *jobs.System
	SSH    *sshx.SSH
	Store  *store.Store
	// Setup renders the version in force; DNS, Usage and RemoteEnv are read at
	// use, because tests replace the module's.
	Setup     func(ctx context.Context, serverID int64) (Setup, error)
	DNS       func() dns.Driver
	Usage     func() UsageReader
	RemoteEnv func(log *jobs.Logger) remote.Env
	Log       *slog.Logger
}

// Service retires servers.
type Service struct {
	Deps
	Now func() time.Time
	// ConnectTimeout bounds the dial of the stack removal (5 s): an
	// unreachable server must not hold retirement up.
	ConnectTimeout time.Duration
	// WaitPoll and WaitTimeout pace the wait for the server's other jobs
	// (every 1 s, up to 15 min).
	WaitPoll, WaitTimeout time.Duration
}

// New returns the service.
func New(d Deps) *Service {
	return &Service{Deps: d, Now: time.Now, ConnectTimeout: 5 * time.Second, WaitPoll: time.Second, WaitTimeout: 15 * time.Minute}
}

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) env(log *jobs.Logger) remote.Env {
	if s.RemoteEnv != nil {
		return s.RemoteEnv(log)
	}
	return remote.Env{Log: log}
}

func serverKey(id int64) string { return "server:" + strconv.FormatInt(id, 10) }

// payload is the job's: what was asked, and what the steps have done.
type payload struct {
	ServerID     int64  `json:"server_id"`
	RemoveStack  bool   `json:"remove_stack"`
	StackRemoved bool   `json:"stack_removed,omitempty"`
	StackSkipped string `json:"stack_skipped,omitempty"`
	// DNSRemoved are the names deleted; DNSKept the records left alone.
	DNSRemoved []string `json:"dns_removed,omitempty"`
	DNSKept    []Kept   `json:"dns_kept,omitempty"`
}

// Kept is a record retirement left alone, and why.
type Kept struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Consequences is what the dialog tells the admin before they confirm.
type Consequences struct {
	Server        store.Server
	Subscriptions []string
	Links         int
	UsageKnown    bool
	DNS           []dns.Record
	StackPossible bool // something was uploaded
	StackDefault  bool // reachable at the last self-check, or a server that never went active
}

// Consequences collects them. It changes nothing.
func (s *Service) Consequences(ctx context.Context, serverID int64) (Consequences, error) {
	srv, err := store.GetServer(ctx, s.DB.R, serverID)
	if err != nil {
		return Consequences{}, err
	}
	if srv.State == "retired" || srv.Retiring() {
		return Consequences{}, ErrNotAllowed
	}
	c := Consequences{Server: srv}
	if s.Usage != nil {
		if u := s.Usage(); u != nil {
			if c.Subscriptions, c.Links, err = u.Usage(ctx, serverID); err != nil {
				return c, err
			}
			c.UsageKnown = true
		}
	}
	recs, err := store.DNSRecords(ctx, s.DB.R, serverID)
	if err != nil {
		return c, err
	}
	for _, r := range recs {
		if !r.Kept.Valid {
			c.DNS = append(c.DNS, toRecord(r))
		}
	}
	if c.StackPossible, err = store.HasUploadedDeployment(ctx, s.DB.R, serverID); err != nil {
		return c, err
	}
	c.StackDefault = c.StackPossible
	if c.StackPossible && srv.State == "active" {
		self, err := store.LatestResults(ctx, s.DB.R, serverID, "self")
		if err != nil {
			return c, err
		}
		for _, r := range self {
			if r.Class == "unreachable" || r.Class == "host-key-changed" {
				c.StackDefault = false
			}
		}
	}
	return c, nil
}

func toRecord(r store.DNSRecord) dns.Record {
	return dns.Record{Provider: r.Provider, ZoneID: r.ZoneID, Zone: r.Zone, Name: r.Name, Type: r.Type, Content: r.Content, RecordID: r.RecordID}
}

// Retire starts the retirement of a provisioning, failed or active server.
// typedName must be the server's name. In one transaction the job is queued
// and the server marked retiring, so from then on it is not offered, not
// checked and not changed.
func (s *Service) Retire(ctx context.Context, serverID int64, typedName string, removeStack bool, actor string) (jobID int64, err error) {
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		srv, err := store.GetServer(ctx, tx, serverID)
		if err != nil {
			return err
		}
		if srv.State == "retired" || srv.Retiring() {
			return ErrNotAllowed
		}
		if strings.TrimSpace(typedName) != srv.Name {
			return ErrNameMismatch
		}
		if removeStack {
			// Nothing was uploaded: there is no stack to remove.
			if ok, err := store.HasUploadedDeployment(ctx, tx, serverID); err != nil {
				return err
			} else if !ok {
				removeStack = false
			}
		}
		// No resource key: it has to start while provisioning still holds the
		// server's; the subject keeps it on the server's Jobs tab.
		e, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
			Type: JobType, Payload: payload{ServerID: serverID, RemoveStack: removeStack},
			Subject: store.ServerSubject(serverID), CreatedBy: actor,
		})
		if err != nil {
			return err
		}
		jobID = e.ID
		if ok, err := store.SetRetireJob(ctx, tx, serverID, jobID); err != nil || !ok {
			if err == nil {
				err = ErrNotAllowed
			}
			return err
		}
		return nil
	})
	if err == nil {
		s.Jobs.Kick()
	}
	return jobID, err
}

// RetryFailed retries a retirement that failed or was cancelled, as a new job
// linked to the old one.
func (s *Service) RetryFailed(ctx context.Context, serverID int64, actor string) (int64, error) {
	var id int64
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		srv, err := store.GetServer(ctx, tx, serverID)
		if err != nil {
			return err
		}
		if !srv.Retiring() {
			return ErrNotRetiring
		}
		if id, err = s.Jobs.RetryWithTx(ctx, tx, srv.RetireJobID.Int64, actor, jobs.RetryOptions{}); err != nil {
			if errors.Is(err, jobs.ErrNotRetryable) {
				return ErrNotRetiring
			}
			return err
		}
		ok, err := store.RetryRetireJob(ctx, tx, serverID, srv.RetireJobID.Int64, id)
		if err == nil && !ok {
			err = ErrNotRetiring
		}
		return err
	})
	if err == nil {
		s.Jobs.Kick()
	}
	return id, err
}

// JobType returns the job type to register.
func (s *Service) JobType() jobs.Type {
	return jobs.Type{
		Name: JobType, Queue: jobs.Provisioning, MaxAttempts: 3,
		Backoff: []time.Duration{time.Minute, 5 * time.Minute},
		Steps: []jobs.Step{
			s.step(StepCancel, s.cancelProvisioning),
			s.step(StepRemoveStack, s.removeStack),
			s.step(StepDNS, s.deleteDNS),
			s.step(StepFinish, s.finish),
		},
		OnFailed: s.onFailed, OnCancelled: s.onCancelled,
	}
}

// step wraps a step: its context ends when the admin cancels, and what a
// cancel breaks becomes ErrCancelled.
func (s *Service) step(name string, fn func(ctx context.Context, r *jobs.Run, p *payload) error) jobs.Step {
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
		var p payload
		err := r.Payload(&p)
		if err == nil {
			err = fn(ctx, r, &p)
		}
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

// --- cancel-provisioning

// cancelProvisioning cancels what has not started and the provisioning that
// runs, then waits until no job runs on the server: a redeploy or a check that
// is already running finishes normally.
func (s *Service) cancelProvisioning(ctx context.Context, r *jobs.Run, p *payload) error {
	key, by := serverKey(p.ServerID), r.Info().Actor()
	deadline := time.NewTimer(s.WaitTimeout)
	defer deadline.Stop()
	told := map[int64]bool{}
	for {
		held, err := s.Jobs.Holding(ctx, key)
		if err != nil {
			return err
		}
		var running []jobs.Held
		for _, h := range held {
			if h.State == jobs.Running {
				running = append(running, h)
			}
			if told[h.ID] {
				continue
			}
			told[h.ID] = true
			// Queued jobs would fail on a retiring server anyway; provisioning
			// has to stop before the server can be retired.
			if h.State != jobs.Running || h.Type == provisionJob {
				r.Log().Info("Cancelling job #%d (%s)", h.ID, h.Type)
				if err := s.Jobs.Cancel(ctx, h.ID, by); err != nil && !errors.Is(err, jobs.ErrNotActive) {
					return err
				}
			} else {
				r.Log().Info("Waiting for job #%d (%s) to finish", h.ID, h.Type)
			}
		}
		if len(running) == 0 {
			r.Log().Info("No other job works on the server")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%s is still busy with job #%d", key, running[0].ID)
		case <-time.After(s.WaitPoll):
		}
	}
}

// --- remove-stack

func (s *Service) removeStack(ctx context.Context, r *jobs.Run, p *payload) error {
	if !p.RemoveStack {
		r.Log().Info("Not asked to remove the stack")
		return nil
	}
	if p.StackRemoved || p.StackSkipped != "" {
		return nil
	}
	setup, err := s.Setup(ctx, p.ServerID)
	if err != nil {
		return fmt.Errorf("render the template version in force (retire without removing the stack to skip this): %w", err)
	}
	srv := setup.Server
	skip := func(why string) error {
		r.Log().Warn("Skipped: unreachable (%s)", why)
		p.StackSkipped = why
		return r.SavePayload(ctx, *p)
	}
	cctx, cancel := context.WithTimeout(ctx, s.ConnectTimeout)
	c, err := s.SSH.Connect(cctx, sshx.Target{
		Hop: sshx.Hop{
			Address: srv.IP + ":" + strconv.Itoa(srv.SSHPort), User: remote.DeployUser, Subject: serverKey(srv.ID),
		},
		FirstContact: sshx.PinOnFirstContact,
	}, r.Log())
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return skip(err.Error())
	}
	defer func() { _ = c.Close() }()
	env := s.env(r.Log())
	for i, st := range setup.Uninstall {
		r.Log().Info("Uninstall step %d of %d: %s", i+1, len(setup.Uninstall), st.Kind)
		if err := remote.RunStep(ctx, env, c, setup.Dir, st); err != nil {
			return err
		}
	}
	if err := remote.RemoveStack(ctx, env, c, setup.Dir); err != nil {
		return err
	}
	p.StackRemoved = true
	return r.SavePayload(ctx, *p)
}

// --- dns

func (s *Service) deleteDNS(ctx context.Context, r *jobs.Run, p *payload) error {
	srv, err := store.GetServer(ctx, s.DB.R, p.ServerID)
	if err != nil {
		return err
	}
	recs, err := store.DNSRecords(ctx, s.DB.R, srv.ID)
	if err != nil {
		return err
	}
	var todo []store.DNSRecord
	for _, rec := range recs {
		if !rec.Kept.Valid {
			todo = append(todo, rec)
		}
	}
	if len(todo) == 0 {
		r.Log().Info("No DNS records to delete")
		return nil
	}
	drv := s.DNS()
	for _, rec := range todo {
		removed, kept, err := drv.Remove(ctx, toRecord(rec), srv.Name, srv.IP)
		if err != nil {
			return fmt.Errorf("delete the DNS record %s: %w", rec.Name, err)
		}
		if removed {
			err = s.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.MarkDNSDeleted(ctx, tx, rec.ID, db.At(s.now())) })
			p.DNSRemoved = append(p.DNSRemoved, rec.Name)
			r.Log().Info("Deleted the DNS record %s", rec.Name)
		} else {
			err = s.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.MarkDNSKept(ctx, tx, rec.ID, kept.Reason) })
			p.DNSKept = append(p.DNSKept, Kept{Name: rec.Name, Reason: kept.Reason})
			r.Log().Warn("Kept the DNS record %s: %s", rec.Name, kept.Reason)
		}
		if err != nil {
			return err
		}
		if err := r.SavePayload(ctx, *p); err != nil {
			return err
		}
	}
	return nil
}

// --- finish

// finish retires the server in one write and forgets its host key after the
// commit. A crash between them is healed by running the step again: on a
// server that is retired already it only forgets the host.
func (s *Service) finish(ctx context.Context, r *jobs.Run, p *payload) error {
	return s.finishServer(ctx, r.Info().Actor(), *p)
}

func (s *Service) finishServer(ctx context.Context, actor string, p payload) error {
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		changed, err := store.MarkRetired(ctx, tx, p.ServerID, db.At(s.now()))
		if err != nil || !changed {
			return err
		}
		removed, kept := p.DNSRemoved, p.DNSKept
		if removed == nil {
			removed = []string{}
		}
		if kept == nil {
			kept = []Kept{}
		}
		_, err = s.Events.Record(ctx, tx, events.Event{
			Type: "server.retired", Subject: store.ServerSubject(p.ServerID), Actor: actor,
			Payload: map[string]any{"dns_removed": removed, "dns_kept": kept, "stack_removed": p.StackRemoved},
		})
		return err
	})
	if err != nil {
		return err
	}
	return s.SSH.ForgetSubject(ctx, serverKey(p.ServerID), actor)
}

// --- failure and cancellation

// onFailed records server.retire_failed. The server stays retiring: its state
// is unchanged and retire_job_id is still set, because the stack may already
// be gone and a half-retired server must not come back into service.
func (s *Service) onFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	step := ""
	_ = tx.GetContext(ctx, &step, `SELECT COALESCE(error_step, '') FROM jobs WHERE id = ?`, j.ID)
	return s.failed(ctx, tx, j, step, cause.Error(), false)
}

func (s *Service) onCancelled(ctx context.Context, tx *sqlx.Tx, j jobs.Info, by string) error {
	step := ""
	_ = tx.GetContext(ctx, &step, `SELECT name FROM job_steps WHERE job_id = ? AND state = 'cancelled' ORDER BY idx LIMIT 1`, j.ID)
	return s.failed(ctx, tx, j, step, "cancelled", true)
}

func (s *Service) failed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, step, errText string, cancelled bool) error {
	if j.Subject.Type != "server" {
		return fmt.Errorf("job %d has no server", j.ID)
	}
	id, err := strconv.ParseInt(j.Subject.ID, 10, 64)
	if err != nil {
		return err
	}
	if len(errText) > maxFailedError {
		errText = errText[:maxFailedError]
	}
	pl := map[string]any{"step": step, "error": errText}
	if cancelled {
		pl["cancelled"] = true
	}
	_, err = s.Events.Record(ctx, tx, events.Event{Type: "server.retire_failed", Subject: store.ServerSubject(id), Actor: j.Actor(), Payload: pl})
	return err
}

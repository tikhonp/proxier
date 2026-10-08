// Package deploy changes a running server: redeploy, upgrade, parameter edits,
// roll back, rolling upgrades, the light stack operations and credential
// rotation (docs/processes/servers/server-redeploy.md and
// credential-rotation.md). Plans are computed here, with the files masked;
// the jobs apply them step by step.
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// Job types.
const (
	JobDeploy        = "servers.deploy"
	JobRestart       = "servers.restart"
	JobUpdateImages  = "servers.update_images"
	JobReboot        = "servers.reboot"
	JobRotate        = "servers.rotate"
	JobRestore       = "servers.restore"
	JobContainerLogs = "servers.container_logs"
)

// Errors a caller can tell apart.
var (
	ErrNotActive       = errors.New("deploy: the server is not active")
	ErrNothingToChange = errors.New("deploy: nothing to change")
	ErrNoRollBack      = errors.New("deploy: there is no failed deployment to roll back")
	ErrNothingToRotate = errors.New("deploy: the template has no rotatable generated value")
	ErrNotAllowed      = errors.New("deploy: not allowed in this state")
)

// FieldErrors and Msg are provisioning's: a refusal names the form field and an
// i18n key. An operation that returns FieldErrors changed nothing.
type (
	FieldErrors = provision.FieldErrors
	Msg         = provision.Msg
)

// maxFailedError is how much of an error an event keeps.
const maxFailedError = 2000

// Seams are what tests replace, shared with provisioning (the module reads
// them from the provision service at use, so one replacement serves both).
type Seams struct {
	// ProxyTest runs one proxy test; ProxyOptions may adjust its options.
	ProxyTest    func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Result
	ProxyOptions func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Options
	// RemoteEnv makes the environment of remote steps.
	RemoteEnv func(log *jobs.Logger) remote.Env
	// Attempts and Gap pace the proxy test (3 tries, 20 s apart).
	Attempts int
	Gap      time.Duration
}

// Deps is what the service needs of the platform and the module.
type Deps struct {
	DB        *db.DB
	Events    *events.Catalog
	Vault     *vault.Vault
	Jobs      *jobs.System
	SSH       *sshx.SSH
	Settings  *settings.Store
	Store     *store.Store
	Templates *templates.Service
	Seams     func() Seams
	Log       *slog.Logger
}

// Service plans and runs changes to active servers.
type Service struct {
	Deps
	Now func() time.Time
	// RebootPoll is the pause between reconnects after a reboot (10 s) and
	// RebootTimeout how long the machine has to come back (5 min).
	RebootPoll, RebootTimeout time.Duration
	// CheckGap is the pause between tries of a self-check that follows a change
	// (5 s): containers that were just recreated need a moment before they answer.
	CheckGap time.Duration
}

// New returns the service.
func New(d Deps) *Service {
	return &Service{Deps: d, Now: time.Now, RebootPoll: 10 * time.Second, RebootTimeout: 5 * time.Minute, CheckGap: 5 * time.Second}
}

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) seams() Seams {
	if s.Seams != nil {
		return s.Seams()
	}
	return Seams{ProxyTest: proxy.Test, Attempts: 3, Gap: 20 * time.Second}
}

func (s *Service) env(log *jobs.Logger) remote.Env {
	if f := s.seams().RemoteEnv; f != nil {
		return f(log)
	}
	return remote.Env{Log: log}
}

// base is what every job's payload says about its server; the callbacks read
// it when they have only the job's id.
type base struct {
	ServerID int64  `json:"server_id"`
	Kind     string `json:"kind"`
	Rollout  int64  `json:"rollout,omitempty"`
	// Restored: a failed rotation put the old files back.
	Restored bool `json:"restored,omitempty"`
}

func serverKey(id int64) string { return "server:" + strconv.FormatInt(id, 10) }

func serverIDOf(j jobs.Info) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(j.ResourceKey, "server:"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("job %d has no server: %w", j.ID, err)
	}
	return id, nil
}

// wrap runs a step with a context that ends when the admin cancels, and turns
// what a cancel breaks into ErrCancelled.
func wrap(fn func(ctx context.Context, r *jobs.Run) error) func(context.Context, *jobs.Run) error {
	return func(ctx context.Context, r *jobs.Run) error {
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
	}
}

func step(name string, fn func(ctx context.Context, r *jobs.Run) error) jobs.Step {
	return jobs.Step{Name: name, Run: wrap(fn)}
}

// JobTypes returns the job types to register.
func (s *Service) JobTypes() []jobs.Type {
	typ := func(name string, steps ...jobs.Step) jobs.Type {
		return jobs.Type{
			Name: name, Queue: jobs.Provisioning, MaxAttempts: 1, // a failure is the admin's to retry
			Steps: steps, OnFailed: s.onFailed, OnCancelled: s.onCancelled,
		}
	}
	rotate := typ(JobRotate,
		step(StepGenerate, s.rotateGenerate),
		step(StepUpload, s.rotateUpload),
		step(StepRedeploy, s.rotateRedeploy),
		step(StepProxyTest, s.rotateProxyTest),
		step(StepCommit, s.rotateCommit),
	)
	logs := typ(JobContainerLogs, step(StepLogs, s.containerLogs))
	logs.OnFailed, logs.OnCancelled = nil, nil // read-only: nothing to record
	return []jobs.Type{
		typ(JobDeploy,
			step(StepConnect, s.deployConnect),
			step(StepPlan, s.deployPlan),
			step(StepGenerated, s.deployGenerated),
			step(StepUpload, s.deployUpload),
			step(StepRedeploy, s.deployRedeploy),
			step(StepEndpoints, s.deployEndpoints),
			step(StepSelfCheck, s.deploySelfCheck),
			step(StepProxyTest, s.deployProxyTest),
			step(StepRecord, s.deployRecord),
		),
		typ(JobRestart, step(StepRestart, s.restart), step(StepSelfCheck, s.opSelfCheck), step(StepRecord, s.opRecord)),
		typ(JobUpdateImages,
			step(StepImagesBefore, s.imagesBefore), step(StepPull, s.pull), step(StepUp, s.up),
			step(StepImagesAfter, s.imagesAfter), step(StepSelfCheck, s.opSelfCheck),
			step(StepProxyTest, s.opProxyTest), step(StepRecord, s.opRecord)),
		typ(JobReboot,
			step(StepReboot, s.reboot), step(StepWaitSSH, s.waitSSH), step(StepSelfCheck, s.opSelfCheck),
			step(StepProxyTest, s.opProxyTest), step(StepRecord, s.opRecord)),
		rotate,
		typ(JobRestore,
			step(StepUpload, s.restoreUpload), step(StepRedeploy, s.restoreRedeploy),
			step(StepProxyTest, s.restoreProxyTest), step(StepClearPending, s.restoreClear)),
		logs,
	}
}

// Step names; titles are the messages job.<type>.step.<name>.
const (
	StepConnect      = "connect"
	StepPlan         = "plan"
	StepGenerated    = "generated-values"
	StepUpload       = "upload-files"
	StepRedeploy     = "redeploy"
	StepEndpoints    = "endpoints"
	StepSelfCheck    = "self-check"
	StepProxyTest    = "proxy-test"
	StepRecord       = "record"
	StepRestart      = "restart"
	StepImagesBefore = "images-before"
	StepPull         = "pull"
	StepUp           = "up"
	StepImagesAfter  = "images-after"
	StepReboot       = "reboot"
	StepWaitSSH      = "wait-ssh"
	StepLogs         = "logs"
	StepGenerate     = "generate"
	StepCommit       = "commit"
	StepClearPending = "clear-pending"
)

// --- failure and cancellation

// payloadOf reads a job's payload and the step it failed at, inside tx.
func payloadOf(ctx context.Context, tx *sqlx.Tx, jobID int64, into any) (step string, err error) {
	var row struct {
		Payload string `db:"payload"`
		Step    string `db:"error_step"`
	}
	if err := tx.GetContext(ctx, &row, `SELECT payload, error_step FROM jobs WHERE id = ?`, jobID); err != nil {
		return "", err
	}
	return row.Step, json.Unmarshal([]byte(row.Payload), into)
}

func (s *Service) onFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	var b base
	step, err := payloadOf(ctx, tx, j.ID, &b)
	if err != nil {
		return err
	}
	return s.stopped(ctx, tx, j, b, step, cause.Error(), false)
}

func (s *Service) onCancelled(ctx context.Context, tx *sqlx.Tx, j jobs.Info, by string) error {
	var b base
	if _, err := payloadOf(ctx, tx, j.ID, &b); err != nil {
		return err
	}
	step := ""
	_ = tx.GetContext(ctx, &step, `SELECT name FROM job_steps WHERE job_id = ? AND state = 'cancelled' ORDER BY idx LIMIT 1`, j.ID)
	if err := s.stopped(ctx, tx, j, b, step, "cancelled", true); err != nil {
		return err
	}
	if j.Type != JobRotate {
		return nil
	}
	// A rotation cancelled after it generated new values may have put some of
	// its files on the server. A remote restore can't run inside this
	// transaction, so it is queued.
	id, err := serverIDOf(j)
	if err != nil {
		return err
	}
	rows, err := store.GeneratedValues(ctx, tx, id)
	if err != nil {
		return err
	}
	pending := false
	for _, r := range rows {
		pending = pending || len(r.Pending) > 0
	}
	if !pending {
		return nil
	}
	e, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobRestore, ResourceKey: serverKey(id), CreatedBy: j.Actor(),
		Payload: base{ServerID: id, Kind: "restore", Rollout: b.Rollout},
	})
	if err != nil {
		return err
	}
	return s.Jobs.LogLine(ctx, tx, j.ID, "info", fmt.Sprintf("The rotation was cancelled: restore queued as job #%d", e.ID))
}

// stopped fails the server's running deployments and records
// server.redeploy_failed. The server stays active.
func (s *Service) stopped(ctx context.Context, tx *sqlx.Tx, j jobs.Info, b base, step, errText string, cancelled bool) error {
	if len(errText) > maxFailedError {
		errText = errText[:maxFailedError]
	}
	if err := store.FailRunningDeployments(ctx, tx, b.ServerID, errText, db.At(s.now())); err != nil {
		return err
	}
	payload := map[string]any{"kind": b.Kind, "step": step, "error": errText}
	if b.Rollout > 0 {
		payload["rollout"] = b.Rollout
	}
	if b.Kind == "rotate" {
		payload["restored"] = b.Restored
	}
	if cancelled {
		payload["cancelled"] = true
	}
	_, err := s.Events.Record(ctx, tx, events.Event{Type: "server.redeploy_failed", Subject: store.ServerSubject(b.ServerID), Actor: j.Actor(), Payload: payload})
	return err
}

// --- shared pieces of the steps

func (s *Service) target(srv store.Server) sshx.Target {
	return sshx.Target{
		Hop: sshx.Hop{
			Address: net.JoinHostPort(srv.IP, strconv.Itoa(srv.SSHPort)), User: remote.DeployUser,
			Subject: serverKey(srv.ID),
		},
		FirstContact: sshx.PinOnFirstContact,
	}
}

// connect opens the deploy user's connection with Proxier's key. A redeploy
// never uses the root password: there is none.
func (s *Service) connect(ctx context.Context, r *jobs.Run, srv store.Server) (*sshx.Client, error) {
	return s.SSH.Connect(ctx, s.target(srv), r.Log())
}

// activeServer loads a server that must be active.
func (s *Service) activeServer(ctx context.Context, q sqlx.QueryerContext, id int64) (store.Server, error) {
	srv, err := store.GetServer(ctx, q, id)
	if err != nil {
		return srv, err
	}
	if srv.State != "active" || srv.Retiring() {
		return srv, ErrNotActive
	}
	return srv, nil
}

// proxyTest tests every endpoint, with the pace and the seams of provisioning.
func (s *Service) proxyTest(ctx context.Context, r *jobs.Run, eps []endpoint.Endpoint, templateURL string) error {
	sm := s.seams()
	if sm.ProxyTest == nil {
		sm.ProxyTest = proxy.Test
	}
	if sm.Attempts < 1 {
		sm.Attempts = 3
	}
	var base proxy.Options
	{
		url, timeout, stall, err := conf.ProxyTest(ctx, s.Settings, templateURL)
		if err != nil {
			return err
		}
		base = proxy.Options{URL: url, Timeout: timeout, Stall: stall}
	}
	for _, e := range eps {
		opts := base
		if sm.ProxyOptions != nil {
			opts = sm.ProxyOptions(ctx, e, opts)
		}
		var last proxy.Result
		for attempt := 1; attempt <= sm.Attempts; attempt++ {
			r.Log().Info("Proxy test through %s, attempt %d of %d", e.Key, attempt, sm.Attempts)
			last = sm.ProxyTest(ctx, e, opts)
			if last.OK {
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
		if !last.OK {
			return fmt.Errorf("the proxy test through %s failed: %s: %s", e.Key, last.Class, last.Error)
		}
	}
	return nil
}

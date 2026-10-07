package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// selfCheckAttempts is how often a change's self-check is tried.
const selfCheckAttempts = 4

// paramSecretPrefix names the job secrets that hold secret parameter values.
const paramSecretPrefix = "param:"

// deployPayload is the payload of servers.deploy.
type deployPayload struct {
	base
	ToVersion int `json:"to_version"`
	// Params are the public values the admin gave; the secret ones are job
	// secrets named param:<key>, listed in SecretParams.
	Params       map[string]string `json:"params,omitempty"`
	SecretParams []string          `json:"secret_params,omitempty"`
	Force        bool              `json:"force,omitempty"`
	PlanHash     string            `json:"plan_hash,omitempty"`
	// Noop: the plan step found nothing to change; the rest of the job does nothing.
	Noop bool `json:"noop,omitempty"`
}

// Apply queues the job that carries out the target. planHash is the hash of
// the plan the admin saw; the job logs when its own plan differs. Nothing is
// queued when there is nothing to change, unless the target is forced.
func (s *Service) Apply(ctx context.Context, serverID int64, t Target, planHash, actor string) (int64, error) {
	c, err := s.computeFor(ctx, serverID, t)
	if err != nil {
		return 0, err
	}
	if len(c.Errs) > 0 {
		return 0, c.Errs
	}
	if c.Plan.Empty {
		return 0, ErrNothingToChange
	}
	var id int64
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, err = s.enqueueDeploy(ctx, tx, c, planHash, 0, actor)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.Jobs.Kick()
	return id, nil
}

// enqueueDeploy queues servers.deploy for a computed target inside tx.
func (s *Service) enqueueDeploy(ctx context.Context, tx *sqlx.Tx, c *computed, planHash string, rollout int64, actor string) (int64, error) {
	p := deployPayload{
		base:      base{ServerID: c.Srv.ID, Kind: c.Plan.Kind, Rollout: rollout},
		ToVersion: c.Plan.ToVersion, Force: c.Target.Force, PlanHash: planHash, Params: map[string]string{},
	}
	secrets := map[string]string{}
	for _, par := range c.Man.Parameters {
		v, ok := c.Target.Params[par.Key]
		if !ok {
			continue
		}
		if par.Secret {
			secrets[paramSecretPrefix+par.Key] = v
			p.SecretParams = append(p.SecretParams, par.Key)
		} else {
			p.Params[par.Key] = v
		}
	}
	e, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobDeploy, Payload: p, Secrets: secrets, ResourceKey: serverKey(c.Srv.ID), CreatedBy: actor,
	})
	return e.ID, err
}

// loadDeploy reads the job's payload and renders its target for the server as
// it is now, registering every secret for redaction before anything is logged.
func (s *Service) loadDeploy(ctx context.Context, r *jobs.Run) (*computed, deployPayload, error) {
	var p deployPayload
	if err := r.Payload(&p); err != nil {
		return nil, p, err
	}
	srv, err := s.activeServer(ctx, s.DB.R, p.ServerID)
	if errors.Is(err, ErrNotActive) {
		return nil, p, jobs.Permanent(fmt.Errorf("the server is not active any more"))
	}
	if err != nil {
		return nil, p, err
	}
	t := Target{Kind: p.Kind, Version: p.ToVersion, Params: map[string]string{}, Force: p.Force}
	for k, v := range p.Params {
		t.Params[k] = v
	}
	for _, k := range p.SecretParams {
		if v, ok, _ := r.Secret(paramSecretPrefix + k); ok {
			t.Params[k] = v
			r.Log().Redact(v)
		}
	}
	c, err := s.compute(ctx, srv, t, nil)
	if err != nil {
		return nil, p, err
	}
	s.redact(r, c)
	return c, p, nil
}

// redact registers every secret a rendered target holds.
func (s *Service) redact(r *jobs.Run, c *computed) {
	for _, v := range c.Gen {
		r.Log().Redact(v)
	}
	for _, v := range c.Secret {
		r.Log().Redact(v)
	}
}

func (s *Service) deployConnect(ctx context.Context, r *jobs.Run) error {
	var p deployPayload
	if err := r.Payload(&p); err != nil {
		return err
	}
	srv, err := s.activeServer(ctx, s.DB.R, p.ServerID)
	if errors.Is(err, ErrNotActive) {
		return jobs.Permanent(errors.New("the server is not active any more"))
	}
	if err != nil {
		return err
	}
	c, err := s.connect(ctx, r, srv)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return remote.VerifyAccess(ctx, s.env(r.Log()), c)
}

func (s *Service) deployPlan(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil {
		return err
	}
	if p.PlanHash != "" && c.Plan.Hash != p.PlanHash {
		r.Log().Warn("The plan was recomputed: %s", c.Plan.summarize())
	} else {
		r.Log().Info("Plan: %s", c.Plan.summarize())
	}
	if len(c.Errs) > 0 {
		return jobs.Permanent(c.Errs)
	}
	if c.Plan.Empty {
		r.Log().Info("Nothing to change any more; the job does nothing")
		p.Noop = true
		return r.SavePayload(ctx, p)
	}
	return nil
}

func (s *Service) deployGenerated(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil || p.Noop {
		return err
	}
	if len(c.Created) == 0 {
		r.Log().Info("No generated value to create")
		return nil
	}
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, k := range c.Created {
			if err := store.PutGenerated(ctx, tx, c.Srv.ID, k, sealed.SealGen(s.Vault, c.Srv.ID, k, c.Gen[k]), db.At(s.now())); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.Log().Info("Generated %d values: %s", len(c.Created), strings.Join(c.Created, ", "))
	return nil
}

// openDeployment returns the running deployment of kind for the job's server,
// made on the first run of the step; a resumed job reuses it.
func (s *Service) openDeployment(ctx context.Context, r *jobs.Run, c *computed, kind string) (int64, error) {
	if dep, ok, err := store.RunningDeployment(ctx, s.DB.R, c.Srv.ID, kind); err != nil {
		return 0, err
	} else if ok {
		return dep.ID, nil
	}
	var id int64
	err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, err = store.InsertDeployment(ctx, tx, store.Deployment{
			ServerID: c.Srv.ID, Kind: kind, TemplateVersion: c.Plan.ToVersion, Params: marshalParams(c.Public),
			JobID: nullInt(r.Info().ID),
		}, db.At(s.now()))
		if err != nil {
			return err
		}
		blob := sealed.SealDeploymentSecrets(s.Vault, id, sealed.DeploymentSecrets{Params: c.Secret, Gen: c.Gen})
		if blob != nil {
			return store.SetDeploymentParamsSecret(ctx, tx, id, blob)
		}
		return nil
	})
	return id, err
}

// storeFiles records the rendered files as the deployment's and marks it as
// one that uploaded.
func (s *Service) storeFiles(ctx context.Context, dep int64, c *computed, changed int) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		rows := make([]store.DeployedFile, len(c.Rendered.Files))
		for i, f := range c.Rendered.Files {
			rows[i] = store.DeployedFile{DeploymentID: dep, Path: f.Path, Mode: int(f.Mode.Perm()), SHA256: sum(f.Content),
				Content: sealed.SealFile(s.Vault, dep, f.Path, f.Content)}
		}
		if err := store.ReplaceDeployedFiles(ctx, tx, dep, rows); err != nil {
			return err
		}
		return store.SetDeploymentUploaded(ctx, tx, dep, changed)
	})
}

func (s *Service) deployUpload(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil || p.Noop {
		return err
	}
	dep, err := s.openDeployment(ctx, r, c, p.Kind)
	if err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := remote.UploadFiles(ctx, s.env(r.Log()), conn, c.Man.Dir, c.Upload, c.Remove); err != nil {
		return err
	}
	changed := c.filesChanged
	if p.Force && len(c.Upload) > changed {
		changed = len(c.Upload)
	}
	return s.storeFiles(ctx, dep, c, changed)
}

// redeploySteps are the version's redeploy steps that are not job steps of
// their own (upload-files is; base-bootstrap belongs to provisioning).
func redeploySteps(c *computed) []manifest.Step {
	var out []manifest.Step
	for _, st := range c.Rendered.Redeploy {
		if st.Kind != "base-bootstrap" && st.Kind != "upload-files" {
			out = append(out, st)
		}
	}
	return out
}

func (s *Service) runSteps(ctx context.Context, r *jobs.Run, c *computed) error {
	steps := redeploySteps(c)
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	for i, st := range steps {
		r.Log().Info("Template step %d of %d: %s", i+1, len(steps), st.Kind)
		if err := remote.RunStep(ctx, s.env(r.Log()), conn, c.Man.Dir, st); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deployRedeploy(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil || p.Noop {
		return err
	}
	return s.runSteps(ctx, r, c)
}

func (s *Service) deployEndpoints(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil || p.Noop {
		return err
	}
	rows := sealed.SealEndpoints(s.Vault, c.Srv.ID, c.Eps)
	for _, row := range rows {
		r.Log().Info("Endpoint %s: %s:%d (%s)", row.Key, row.Host, row.Port, row.DisplayName)
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return store.ReplaceEndpoints(ctx, tx, c.Srv.ID, rows, db.At(s.now()))
	})
}

func (s *Service) deploySelfCheck(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil || p.Noop {
		return err
	}
	return s.selfCheck(ctx, r, c, selfCheckAttempts)
}

// selfCheck runs the version's checks, trying again a few times (containers
// that were just recreated or restarted need a moment) before it reports the
// ones that fail.
func (s *Service) selfCheck(ctx context.Context, r *jobs.Run, c *computed, attempts int) error {
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	gap := s.CheckGap
	for attempt := 1; ; attempt++ {
		err = remote.SelfCheckReport(ctx, s.env(r.Log()), conn, c.Man.Dir, c.Rendered.Checks, remote.DefaultThresholds)
		if err == nil || attempt >= attempts || ctx.Err() != nil {
			return err
		}
		var exit *remote.ExitError
		if errors.As(err, &exit) {
			return err
		}
		r.Log().Warn("Self-check attempt %d of %d failed; trying again in %s", attempt, attempts, gap)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gap):
		}
	}
}

func (s *Service) deployProxyTest(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil || p.Noop {
		return err
	}
	return s.proxyTest(ctx, r, c.Eps, c.Rendered.ProxyTestURL)
}

func (s *Service) deployRecord(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadDeploy(ctx, r)
	if err != nil || p.Noop {
		return err
	}
	dep, ok, err := store.RunningDeployment(ctx, s.DB.R, c.Srv.ID, p.Kind)
	if err != nil {
		return err
	}
	if !ok {
		r.Log().Info("The deployment is recorded already")
		return nil
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := db.At(s.now())
		if err := store.FinishDeployment(ctx, tx, dep.ID, "succeeded", "", now); err != nil {
			return err
		}
		if err := store.SetServerParams(ctx, tx, c.Srv.ID, c.Plan.ToVersion, marshalParams(c.Public), sealed.SealParams(s.Vault, c.Srv.ID, c.Secret)); err != nil {
			return err
		}
		payload := map[string]any{"kind": p.Kind, "from_version": c.Plan.FromVersion, "to_version": c.Plan.ToVersion, "files_changed": dep.FilesChanged}
		if p.Rollout > 0 {
			payload["rollout"] = p.Rollout
		}
		_, err := s.Events.Record(ctx, tx, events.Event{Type: "server.redeployed", Subject: store.ServerSubject(c.Srv.ID), Actor: r.Info().Actor(), Payload: payload})
		return err
	})
}

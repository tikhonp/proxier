package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/gen"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// RotatableKeys lists the generated values a rotation of the server replaces:
// those its version marks rotate: true.
func (s *Service) RotatableKeys(ctx context.Context, serverID int64) ([]string, error) {
	srv, err := s.activeServer(ctx, s.DB.R, serverID)
	if err != nil {
		return nil, err
	}
	man, _, err := s.parseVersion(ctx, srv.TemplateID, srv.TemplateVersion)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, g := range man.Generated {
		if g.Rotate {
			keys = append(keys, g.Key)
		}
	}
	return keys, nil
}

// Rotate queues a rotation: new rotatable values are generated, deployed and
// proxy-tested before they replace the old ones.
func (s *Service) Rotate(ctx context.Context, serverID int64, actor string) (int64, error) {
	keys, err := s.RotatableKeys(ctx, serverID)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, ErrNothingToRotate
	}
	e, err := s.Jobs.EnqueueNow(ctx, jobs.Request{
		Type: JobRotate, Payload: base{ServerID: serverID, Kind: "rotate"}, ResourceKey: serverKey(serverID), CreatedBy: actor,
	})
	return e.ID, err
}

// loadRotation renders the version in force with the pending values over the
// current ones.
func (s *Service) loadRotation(ctx context.Context, r *jobs.Run, withPending bool) (*computed, base, error) {
	var b base
	if err := r.Payload(&b); err != nil {
		return nil, b, err
	}
	srv, err := s.activeServer(ctx, s.DB.R, b.ServerID)
	if errors.Is(err, ErrNotActive) {
		return nil, b, jobs.Permanent(errors.New("the server is not active any more"))
	}
	if err != nil {
		return nil, b, err
	}
	rows, err := store.GeneratedValues(ctx, s.DB.R, srv.ID)
	if err != nil {
		return nil, b, err
	}
	pending, err := sealed.OpenPending(s.Vault, srv.ID, rows)
	if err != nil {
		return nil, b, err
	}
	for _, v := range pending {
		r.Log().Redact(v)
	}
	var overlay map[string]string
	if withPending {
		overlay = pending
	}
	c, err := s.compute(ctx, srv, Target{}, overlay)
	if err != nil {
		return nil, b, err
	}
	s.redact(r, c)
	for _, v := range c.OldGen {
		r.Log().Redact(v)
	}
	return c, b, nil
}

func (s *Service) rotateGenerate(ctx context.Context, r *jobs.Run) error {
	c, _, err := s.loadRotation(ctx, r, false)
	if err != nil {
		return err
	}
	fresh := map[string]string{}
	for _, g := range c.Man.Generated {
		if !g.Rotate {
			continue
		}
		v, err := gen.New(g)
		if err != nil {
			return err
		}
		r.Log().Redact(v)
		fresh[g.Key] = v
	}
	if len(fresh) == 0 {
		return jobs.Permanent(ErrNothingToRotate)
	}
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		// A new rotation always starts from new values; a resumed one does not
		// get here again, because this step has succeeded.
		if err := store.ClearPending(ctx, tx, c.Srv.ID); err != nil {
			return err
		}
		for k, v := range fresh {
			ok, err := store.SetPending(ctx, tx, c.Srv.ID, k, sealed.SealGen(s.Vault, c.Srv.ID, k, v))
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("the server has no generated value %q to rotate", k)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	keys := sortedKeys(fresh)
	r.Log().Info("Generated new values for %s; the old ones stay in force until the new ones pass the proxy test", strings.Join(keys, ", "))
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// restoreOnError puts the old files back when a rotation step failed, unless
// the admin cancelled or the process is shutting down (a cancelled rotation is
// restored by its own job; a shut-down one resumes). It returns err.
func (s *Service) restoreOnError(ctx context.Context, r *jobs.Run, err error) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	select {
	case <-r.Cancelling():
		return err
	default:
	}
	var b base
	if perr := r.Payload(&b); perr != nil {
		return err
	}
	r.Log().Error("The rotation failed: %v", err)
	r.Log().Info("Restoring the old files")
	rerr := s.restoreInline(ctx, r)
	b.Restored = rerr == nil
	if rerr != nil {
		r.Log().Error("The restore failed too: %v. The server may hold new files; its old credentials are still the ones Proxier serves. Use Redeploy to put the current files back.", rerr)
	} else {
		r.Log().Info("The old files are back and the old values work")
	}
	if serr := r.SavePayload(context.WithoutCancel(ctx), b); serr != nil {
		r.Log().Warn("Could not note the restore in the job: %v", serr)
	}
	return err
}

func (s *Service) rotateUpload(ctx context.Context, r *jobs.Run) error {
	return s.restoreOnError(ctx, r, s.rotateUploadStep(ctx, r))
}

func (s *Service) rotateUploadStep(ctx context.Context, r *jobs.Run) error {
	c, _, err := s.loadRotation(ctx, r, true)
	if err != nil {
		return err
	}
	dep, err := s.openDeployment(ctx, r, c, "rotate")
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
	return s.storeFiles(ctx, dep, c, c.filesChanged)
}

func (s *Service) rotateRedeploy(ctx context.Context, r *jobs.Run) error {
	return s.restoreOnError(ctx, r, func() error {
		c, _, err := s.loadRotation(ctx, r, true)
		if err != nil {
			return err
		}
		return s.runSteps(ctx, r, c)
	}())
}

func (s *Service) rotateProxyTest(ctx context.Context, r *jobs.Run) error {
	return s.restoreOnError(ctx, r, func() error {
		c, _, err := s.loadRotation(ctx, r, true)
		if err != nil {
			return err
		}
		r.Log().Info("Testing the new credentials through every endpoint")
		return s.proxyTest(ctx, r, c.Eps, c.Rendered.ProxyTestURL)
	}())
}

func (s *Service) rotateCommit(ctx context.Context, r *jobs.Run) error {
	c, _, err := s.loadRotation(ctx, r, true)
	if err != nil {
		return err
	}
	rows, err := store.GeneratedValues(ctx, s.DB.R, c.Srv.ID)
	if err != nil {
		return err
	}
	pending, err := sealed.OpenPending(s.Vault, c.Srv.ID, rows)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		r.Log().Info("The new values are in force already")
		return nil
	}
	dep, hasDep, err := store.RunningDeployment(ctx, s.DB.R, c.Srv.ID, "rotate")
	if err != nil {
		return err
	}
	epRows := sealed.SealEndpoints(s.Vault, c.Srv.ID, c.Eps)
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := db.At(s.now())
		keys, err := store.CommitPending(ctx, tx, c.Srv.ID, now)
		if err != nil {
			return err
		}
		if err := store.ReplaceEndpoints(ctx, tx, c.Srv.ID, epRows, now); err != nil {
			return err
		}
		if hasDep {
			if err := store.FinishDeployment(ctx, tx, dep.ID, "succeeded", "", now); err != nil {
				return err
			}
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Type: "server.credentials_rotated", Subject: store.ServerSubject(c.Srv.ID), Actor: r.Info().Actor(), Payload: map[string]any{"keys": keys}})
		return err
	})
}

// --- restore

// restoreCtx is what a restore works from: the server's current files and
// values, which the failed or cancelled rotation never replaced.
type restoreCtx struct {
	c   *computed
	cur *current
}

func (s *Service) loadRestore(ctx context.Context, r *jobs.Run) (*restoreCtx, error) {
	c, _, err := s.loadRotation(ctx, r, false)
	if err != nil {
		return nil, err
	}
	if c.Cur == nil {
		return nil, jobs.Permanent(errors.New("the server has no current files to restore"))
	}
	return &restoreCtx{c: c, cur: c.Cur}, nil
}

// restoreInline restores inside a failing rotation step.
func (s *Service) restoreInline(ctx context.Context, r *jobs.Run) (err error) {
	rc, err := s.loadRestore(ctx, r)
	if err != nil {
		return err
	}
	defer func() {
		// The rotation is abandoned either way; its values must not linger.
		if cerr := s.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.ClearPending(ctx, tx, rc.c.Srv.ID) }); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if err = s.restoreUploadFiles(ctx, r, rc); err != nil {
		return err
	}
	if err = s.runSteps(ctx, r, rc.c); err != nil {
		return err
	}
	if err = s.restoreTest(ctx, r, rc); err != nil {
		return err
	}
	return s.finishRestore(ctx, r, rc, false)
}

func (s *Service) restoreUpload(ctx context.Context, r *jobs.Run) error {
	rc, err := s.loadRestore(ctx, r)
	if err != nil {
		return err
	}
	return s.restoreUploadFiles(ctx, r, rc)
}

func (s *Service) restoreRedeploy(ctx context.Context, r *jobs.Run) error {
	rc, err := s.loadRestore(ctx, r)
	if err != nil {
		return err
	}
	return s.runSteps(ctx, r, rc.c)
}

func (s *Service) restoreProxyTest(ctx context.Context, r *jobs.Run) error {
	rc, err := s.loadRestore(ctx, r)
	if err != nil {
		return err
	}
	return s.restoreTest(ctx, r, rc)
}

func (s *Service) restoreClear(ctx context.Context, r *jobs.Run) error {
	rc, err := s.loadRestore(ctx, r)
	if err != nil {
		return err
	}
	return s.finishRestore(ctx, r, rc, true)
}

// restoreUploadFiles uploads the current files again, in a deployment of kind
// restore, and removes what the abandoned rotation put on the server besides.
func (s *Service) restoreUploadFiles(ctx context.Context, r *jobs.Run, rc *restoreCtx) error {
	c, cur := rc.c, rc.cur
	var depID int64
	if dep, ok, err := store.RunningDeployment(ctx, s.DB.R, c.Srv.ID, "restore"); err != nil {
		return err
	} else if ok {
		depID = dep.ID
	} else {
		err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			depID, err = store.InsertDeployment(ctx, tx, store.Deployment{
				ServerID: c.Srv.ID, Kind: "restore", TemplateVersion: cur.Dep.TemplateVersion, Params: cur.Dep.Params, JobID: nullInt(r.Info().ID),
			}, db.At(s.now()))
			if err != nil {
				return err
			}
			if blob := sealed.SealDeploymentSecrets(s.Vault, depID, cur.Secrets); blob != nil {
				return store.SetDeploymentParamsSecret(ctx, tx, depID, blob)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(cur.Files))
	for p := range cur.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	files := make([]remote.File, len(paths))
	for i, p := range paths {
		files[i] = remote.File{Path: p, Mode: cur.Files[p].Mode, Content: cur.Files[p].Content}
	}
	// Files an abandoned deployment added that the current set lacks.
	var extra []string
	later, err := store.UploadedAfter(ctx, s.DB.R, c.Srv.ID, cur.Dep.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, d := range later {
		rows, err := store.DeployedFiles(ctx, s.DB.R, d.ID)
		if err != nil {
			return err
		}
		for _, f := range rows {
			if _, ok := cur.Files[f.Path]; !ok && !seen[f.Path] {
				seen[f.Path] = true
				extra = append(extra, f.Path)
			}
		}
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	r.Log().Info("Uploading the %d files of the current deployment again", len(files))
	if _, err := remote.UploadFiles(ctx, s.env(r.Log()), conn, c.Man.Dir, files, extra); err != nil {
		return err
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		rows := make([]store.DeployedFile, len(files))
		for i, f := range files {
			rows[i] = store.DeployedFile{DeploymentID: depID, Path: f.Path, Mode: int(f.Mode.Perm()), SHA256: sum(f.Content),
				Content: sealed.SealFile(s.Vault, depID, f.Path, f.Content)}
		}
		if err := store.ReplaceDeployedFiles(ctx, tx, depID, rows); err != nil {
			return err
		}
		return store.SetDeploymentUploaded(ctx, tx, depID, len(files))
	})
}

// restoreTest proxy-tests the endpoints Proxier serves: the old values.
func (s *Service) restoreTest(ctx context.Context, r *jobs.Run, rc *restoreCtx) error {
	eps, err := s.storedEndpoints(ctx, rc.c.Srv.ID)
	if err != nil {
		return err
	}
	r.Log().Info("Testing the old credentials through every endpoint")
	return s.proxyTest(ctx, r, eps, rc.c.Rendered.ProxyTestURL)
}

// finishRestore drops the pending values and closes the restore deployment.
// As a job of its own it also records server.redeployed{kind: restore}.
func (s *Service) finishRestore(ctx context.Context, r *jobs.Run, rc *restoreCtx, record bool) error {
	c := rc.c
	dep, ok, err := store.RunningDeployment(ctx, s.DB.R, c.Srv.ID, "restore")
	if err != nil {
		return err
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := store.ClearPending(ctx, tx, c.Srv.ID); err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := store.FinishDeployment(ctx, tx, dep.ID, "succeeded", "", db.At(s.now())); err != nil {
			return err
		}
		if !record {
			return nil
		}
		_, err := s.Events.Record(ctx, tx, events.Event{Type: "server.redeployed", Subject: store.ServerSubject(c.Srv.ID), Actor: r.Info().Actor(),
			Payload: map[string]any{"kind": "restore", "from_version": c.Srv.TemplateVersion, "to_version": c.Srv.TemplateVersion, "files_changed": dep.FilesChanged}})
		return err
	})
}

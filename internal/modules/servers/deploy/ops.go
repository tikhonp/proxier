package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// opPayload is the payload of the light operations.
type opPayload struct {
	base
	// Before holds the images of the stack before an image update.
	Before map[string]remote.Image `json:"before,omitempty"`
	// Changed names the images an update changed.
	Changed []string `json:"changed_images,omitempty"`
	// BootID is the boot before a reboot.
	BootID string `json:"boot_id,omitempty"`
}

// Restart queues a restart of the stack (docker compose restart), then a self-check.
func (s *Service) Restart(ctx context.Context, serverID int64, actor string) (int64, error) {
	return s.enqueueOp(ctx, serverID, JobRestart, "restart", actor)
}

// UpdateImages pulls the newest images and recreates what changed.
func (s *Service) UpdateImages(ctx context.Context, serverID int64, actor string) (int64, error) {
	return s.enqueueOp(ctx, serverID, JobUpdateImages, "images", actor)
}

// Reboot restarts the machine and waits for it.
func (s *Service) Reboot(ctx context.Context, serverID int64, actor string) (int64, error) {
	return s.enqueueOp(ctx, serverID, JobReboot, "reboot", actor)
}

// ContainerLogs queues a read-only job that writes the last 200 lines of every
// service into its log. It is allowed on an active server, and on a failed one
// whose stack was uploaded.
func (s *Service) ContainerLogs(ctx context.Context, serverID int64, actor string) (int64, error) {
	srv, err := store.GetServer(ctx, s.DB.R, serverID)
	if err != nil {
		return 0, err
	}
	switch srv.State {
	case "active":
	case "failed":
		if ok, err := store.HasUploadedDeployment(ctx, s.DB.R, serverID); err != nil {
			return 0, err
		} else if !ok {
			return 0, ErrNotAllowed
		}
	default:
		return 0, ErrNotAllowed
	}
	e, err := s.Jobs.EnqueueNow(ctx, jobs.Request{
		Type: JobContainerLogs, Payload: base{ServerID: serverID, Kind: "logs"}, ResourceKey: serverKey(serverID), CreatedBy: actor,
	})
	return e.ID, err
}

func (s *Service) enqueueOp(ctx context.Context, serverID int64, typ, kind, actor string) (int64, error) {
	if _, err := s.activeServer(ctx, s.DB.R, serverID); err != nil {
		return 0, err
	}
	e, err := s.Jobs.EnqueueNow(ctx, jobs.Request{
		Type: typ, Payload: opPayload{base: base{ServerID: serverID, Kind: kind}}, ResourceKey: serverKey(serverID), CreatedBy: actor,
	})
	return e.ID, err
}

// loadOp reads the payload of an operation and renders the version in force.
func (s *Service) loadOp(ctx context.Context, r *jobs.Run) (*computed, opPayload, error) {
	var p opPayload
	if err := r.Payload(&p); err != nil {
		return nil, p, err
	}
	srv, err := s.activeServer(ctx, s.DB.R, p.ServerID)
	if errors.Is(err, ErrNotActive) {
		return nil, p, jobs.Permanent(errors.New("the server is not active any more"))
	}
	if err != nil {
		return nil, p, err
	}
	c, err := s.compute(ctx, srv, Target{}, nil)
	if err != nil {
		return nil, p, err
	}
	s.redact(r, c)
	return c, p, nil
}

// openOp makes the running deployment of an operation (no files), once.
func (s *Service) openOp(ctx context.Context, r *jobs.Run, c *computed, kind string) error {
	if _, ok, err := store.RunningDeployment(ctx, s.DB.R, c.Srv.ID, kind); err != nil || ok {
		return err
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := store.InsertDeployment(ctx, tx, store.Deployment{
			ServerID: c.Srv.ID, Kind: kind, TemplateVersion: c.Srv.TemplateVersion, Params: c.Srv.Params, JobID: nullInt(r.Info().ID),
		}, db.At(s.now()))
		return err
	})
}

func (s *Service) restart(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	if err := s.openOp(ctx, r, c, p.Kind); err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return remote.RestartStack(ctx, s.env(r.Log()), conn, c.Man.Dir)
}

func (s *Service) opSelfCheck(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	attempts := selfCheckAttempts
	if p.Kind == "reboot" {
		attempts *= 3 // docker starts after SSH does
	}
	return s.selfCheck(ctx, r, c, attempts)
}

// storedEndpoints are the endpoints clients use now.
func (s *Service) storedEndpoints(ctx context.Context, serverID int64) ([]endpoint.Endpoint, error) {
	rows, err := store.Endpoints(ctx, s.DB.R, serverID)
	if err != nil {
		return nil, err
	}
	return sealed.OpenEndpoints(s.Vault, rows)
}

func (s *Service) opProxyTest(ctx context.Context, r *jobs.Run) error {
	c, _, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	eps, err := s.storedEndpoints(ctx, c.Srv.ID)
	if err != nil {
		return err
	}
	return s.proxyTest(ctx, r, eps, c.Rendered.ProxyTestURL)
}

func (s *Service) opRecord(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadOp(ctx, r)
	if err != nil {
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
		if err := store.FinishDeployment(ctx, tx, dep.ID, "succeeded", "", db.At(s.now())); err != nil {
			return err
		}
		payload := map[string]any{"kind": p.Kind, "from_version": c.Srv.TemplateVersion, "to_version": c.Srv.TemplateVersion, "files_changed": 0}
		if p.Kind == "images" {
			changed := p.Changed
			if changed == nil {
				changed = []string{}
			}
			payload["changed_images"] = changed
		}
		_, err := s.Events.Record(ctx, tx, events.Event{Type: "server.redeployed", Subject: store.ServerSubject(c.Srv.ID), Actor: r.Info().Actor(), Payload: payload})
		return err
	})
}

// --- update images

func (s *Service) imagesBefore(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	if err := s.openOp(ctx, r, c, p.Kind); err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if p.Before, err = remote.ComposeImages(ctx, s.env(r.Log()), conn, c.Man.Dir); err != nil {
		return err
	}
	for _, im := range p.Before {
		r.Log().Info("Image of %s: %s %s", im.Container, im.Name(), im.ID)
	}
	return r.SavePayload(ctx, p)
}

func (s *Service) pull(ctx context.Context, r *jobs.Run) error {
	c, _, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return remote.PullImages(ctx, s.env(r.Log()), conn, c.Man.Dir)
}

func (s *Service) up(ctx context.Context, r *jobs.Run) error {
	c, _, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return remote.StartStack(ctx, s.env(r.Log()), conn, c.Man.Dir)
}

func (s *Service) imagesAfter(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	after, err := remote.ComposeImages(ctx, s.env(r.Log()), conn, c.Man.Dir)
	if err != nil {
		return err
	}
	p.Changed = remote.ChangedImages(p.Before, after)
	if len(p.Changed) == 0 {
		r.Log().Info("No image changed")
	} else {
		r.Log().Info("Changed images: %v", p.Changed)
	}
	return r.SavePayload(ctx, p)
}

// --- reboot

func (s *Service) reboot(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	if err := s.openOp(ctx, r, c, p.Kind); err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, c.Srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	env := s.env(r.Log())
	if p.BootID, err = remote.BootID(ctx, env, conn); err != nil {
		return err
	}
	if err := r.SavePayload(ctx, p); err != nil {
		return err
	}
	return remote.Reboot(ctx, env, conn)
}

// waitSSH reconnects until the machine answers on a boot other than the one it
// had, or the timeout passes. A changed host key stops the wait.
func (s *Service) waitSSH(ctx context.Context, r *jobs.Run) error {
	c, p, err := s.loadOp(ctx, r)
	if err != nil {
		return err
	}
	timeout, poll := s.RebootTimeout, s.RebootPoll
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if poll <= 0 {
		poll = 10 * time.Second
	}
	env := s.env(r.Log())
	deadline := time.Now().Add(timeout)
	r.Log().Info("Waiting for %s to come back (up to %s)", c.Srv.Name, timeout)
	last := ""
	for {
		note := ""
		conn, err := s.connect(ctx, r, c.Srv)
		if err == nil {
			id, berr := remote.BootID(ctx, env, conn)
			_ = conn.Close()
			switch {
			case berr != nil:
				note = "connected, but the boot id could not be read: " + berr.Error()
			case id == p.BootID && p.BootID != "":
				note = "still the same boot"
			default:
				r.Log().Info("The server is back")
				return nil
			}
		} else {
			var changed *sshx.HostKeyChangedError
			if errors.As(err, &changed) {
				return err
			}
			note = "not reachable: " + err.Error()
		}
		if note != last {
			r.Log().Info("%s", note)
			last = note
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the server did not come back within %s (%s)", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// --- container logs

func (s *Service) containerLogs(ctx context.Context, r *jobs.Run) error {
	var p base
	if err := r.Payload(&p); err != nil {
		return err
	}
	srv, err := store.GetServer(ctx, s.DB.R, p.ServerID)
	if err != nil {
		return err
	}
	if srv.State != "active" && srv.State != "failed" {
		return jobs.Permanent(errors.New("the server has no stack"))
	}
	secrets, err := s.StoredSecrets(ctx, srv, nil)
	if err != nil {
		return err
	}
	for _, vals := range secrets {
		r.Log().Redact(vals...)
	}
	man, _, err := s.parseVersion(ctx, srv.TemplateID, srv.TemplateVersion)
	if err != nil {
		return err
	}
	conn, err := s.connect(ctx, r, srv)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	env := s.env(r.Log())
	services, err := remote.ComposeServices(ctx, env, conn, man.Dir)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		r.Log().Info("The stack has no service")
	}
	for _, name := range services {
		if err := remote.ServiceLogs(ctx, env, conn, man.Dir, name); err != nil {
			return err
		}
	}
	return nil
}

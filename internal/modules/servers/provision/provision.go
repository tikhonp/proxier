package provision

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
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/gen"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// JobType is the provisioning job.
const JobType = "servers.provision"

// rootPasswordSecret is the job secret that holds root's password until the
// deploy user's key login works.
const rootPasswordSecret = "root_password"

// maxFailedError is how much of an error the server row and the event keep.
const maxFailedError = 2000

// Step names, in order. They are the job's steps, stored and matched on resume.
const (
	StepPreflight     = "preflight"
	StepInstallAccess = "install-access"
	StepBootstrap     = "base-bootstrap"
	StepDNS           = "dns"
	StepGenerated     = "generated-values"
	StepUpload        = "upload-files"
	StepInstall       = "install"
	StepEndpoints     = "endpoints"
	StepSelfCheck     = "self-check"
	StepSmokeTest     = "smoke-test"
	StepActivate      = "activate"
)

// Deps is what provisioning needs of the platform and the module.
type Deps struct {
	DB        *db.DB
	Events    *events.Catalog
	Vault     *vault.Vault
	Jobs      *jobs.System
	SSH       *sshx.SSH
	Settings  *settings.Store
	Store     *store.Store
	Templates *templates.Service
	// DNS and Waiter are read at use, because tests replace the module's.
	DNS    func() dns.Driver
	Waiter func() dns.Waiter
	Log    *slog.Logger
}

// Service provisions servers.
type Service struct {
	Deps
	Now func() time.Time

	// ProxyTest runs one proxy test; the default is proxy.Test. ProxyOptions
	// may adjust the options (tests point the endpoint at an in-process server).
	ProxyTest    func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Result
	ProxyOptions func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Options
	// RemoteEnv makes the environment of remote steps (waits are paced there);
	// SmokeAttempts and SmokeGap pace the smoke test (3 tries, 20 s apart).
	RemoteEnv     func(log *jobs.Logger) remote.Env
	SmokeAttempts int
	SmokeGap      time.Duration

	geo geoCache
}

// New returns the service.
func New(d Deps) *Service {
	return &Service{Deps: d, Now: time.Now, ProxyTest: proxy.Test, SmokeAttempts: 3, SmokeGap: 20 * time.Second}
}

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) env(log *jobs.Logger) remote.Env {
	if s.RemoteEnv != nil {
		return s.RemoteEnv(log)
	}
	return remote.Env{Log: log}
}

// JobType returns the job type to register.
func (s *Service) JobType() jobs.Type {
	step := func(name string, fn func(ctx context.Context, r *jobs.Run, d *data) error) jobs.Step {
		return jobs.Step{Name: name, Run: s.step(fn)}
	}
	return jobs.Type{
		Name: JobType, Queue: jobs.Provisioning, MaxAttempts: 1, // a failure is the admin's to retry: retries ask for input
		Steps: []jobs.Step{
			step(StepPreflight, s.preflight),
			step(StepInstallAccess, s.installAccess),
			step(StepBootstrap, s.baseBootstrap),
			step(StepDNS, s.dnsStep),
			step(StepGenerated, s.generatedValues),
			step(StepUpload, s.uploadFiles),
			step(StepInstall, s.install),
			step(StepEndpoints, s.endpointsStep),
			step(StepSelfCheck, s.selfCheck),
			step(StepSmokeTest, s.smokeTest),
			step(StepActivate, s.activate),
		},
		OnFailed:    s.onFailed,
		OnCancelled: s.onCancelled,
	}
}

// data is everything a step reads about its server, loaded fresh by every step
// so a resumed job sees what earlier steps and earlier runs stored.
type data struct {
	Payload jobPayload
	Server  store.Server
	Loc     store.Location
	Slug    string
	Files   map[string][]byte
	Man     *manifest.Manifest
	Params  map[string]string // public and secret
	Gen     map[string]string
}

// step wraps a step function: it loads the server, ends the step's context
// when the admin cancels, and turns what a cancel breaks into ErrCancelled.
func (s *Service) step(fn func(ctx context.Context, r *jobs.Run, d *data) error) func(context.Context, *jobs.Run) error {
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
		d, err := s.load(ctx, r)
		if err == nil {
			err = fn(ctx, r, d)
		}
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

// load reads the server and its template version, and registers every
// generated value and secret parameter for redaction before any remote call.
func (s *Service) load(ctx context.Context, r *jobs.Run) (*data, error) {
	d := &data{}
	if err := r.Payload(&d.Payload); err != nil {
		return nil, err
	}
	srv, err := store.GetServer(ctx, s.DB.R, d.Payload.ServerID)
	if err != nil {
		return nil, fmt.Errorf("load server %d: %w", d.Payload.ServerID, err)
	}
	d.Server = srv
	if d.Loc, err = s.Store.Location(ctx, srv.LocationID); err != nil {
		return nil, err
	}
	tpl, err := store.GetTemplate(ctx, s.DB.R, srv.TemplateID)
	if err != nil {
		return nil, err
	}
	d.Slug = tpl.Slug
	v, err := s.Templates.Version(ctx, srv.TemplateID, srv.TemplateVersion)
	if err != nil {
		return nil, fmt.Errorf("load template version %d: %w", srv.TemplateVersion, err)
	}
	d.Files = v.Files
	m, fs := manifest.Parse(v.Files[manifest.Name])
	if m == nil {
		return nil, fmt.Errorf("the manifest of version %d does not parse: %v", srv.TemplateVersion, fs)
	}
	d.Man = m

	d.Params = store.ParseParams(srv.Params)
	secret, err := sealed.OpenParams(s.Vault, srv.ID, srv.ParamsSecret)
	if err != nil {
		return nil, err
	}
	for k, val := range secret {
		d.Params[k] = val
		r.Log().Redact(val)
	}
	rows, err := store.GeneratedValues(ctx, s.DB.R, srv.ID)
	if err != nil {
		return nil, err
	}
	if d.Gen, err = sealed.OpenGenerated(s.Vault, srv.ID, rows); err != nil {
		return nil, err
	}
	for _, val := range d.Gen {
		r.Log().Redact(val)
	}
	return d, nil
}

// renderContext is what templates see for this server.
func (d *data) renderContext() render.Context {
	params := map[string]any{}
	for _, p := range d.Man.Parameters {
		if raw, ok := d.Params[p.Key]; ok {
			params[p.Key] = render.ParamValue(p, raw)
		}
	}
	return render.Context{
		Server: render.Server{
			Name: d.Server.Name, Number: d.Server.Number, IP: d.Server.IP, SSHPort: d.Server.SSHPort,
			Location:           render.Location{Code: d.Loc.Code, Name: d.Loc.Name, Country: d.Loc.Country},
			ManagementHostname: d.Server.ManagementHostname, ProxyHostname: d.Server.ProxyHostname,
		},
		Params: params, Gen: d.Gen, Template: render.Template{Slug: d.Slug, Version: d.Server.TemplateVersion},
	}
}

// render renders the version for the server; the first problem is the error.
func (d *data) render() (*render.Rendered, error) {
	out, fs := render.Render(d.Man, d.Files, d.renderContext())
	for _, f := range fs {
		if f.Severity == "error" {
			return nil, fmt.Errorf("rendering the template: %s", f)
		}
	}
	return out, nil
}

func (s *Service) target(srv store.Server, user, password string) sshx.Target {
	return sshx.Target{
		Hop: sshx.Hop{
			Address: net.JoinHostPort(srv.IP, strconv.Itoa(srv.SSHPort)), User: user, Password: password,
			Subject: "server:" + strconv.FormatInt(srv.ID, 10),
		},
		FirstContact: sshx.PinOnFirstContact,
	}
}

// connect opens the deploy user's connection with Proxier's key.
func (s *Service) connect(ctx context.Context, r *jobs.Run, srv store.Server) (*sshx.Client, error) {
	return s.SSH.Connect(ctx, s.target(srv, remote.DeployUser, ""), r.Log())
}

// rootErr names a refused root login for what it is.
func rootErr(err error) error {
	if errors.Is(err, sshx.ErrAuth) {
		return jobs.Permanent(errors.New("authentication failed: the server refused the root password"))
	}
	return err
}

// --- the steps

func (s *Service) preflight(ctx context.Context, r *jobs.Run, d *data) error {
	pw, ok, _ := r.Secret(rootPasswordSecret)
	if !ok || pw == "" {
		return jobs.Permanent(errors.New("the root password is needed; use Retry and enter it"))
	}
	r.Log().Info("Connecting to %s:%d as root", d.Server.IP, d.Server.SSHPort)
	c, err := s.SSH.Connect(ctx, s.target(d.Server, "root", pw), r.Log())
	if err != nil {
		return rootErr(err)
	}
	defer func() { _ = c.Close() }()
	_, err = remote.Preflight(ctx, s.env(r.Log()), c, d.Man.Requires, d.Man.Ports)
	return err
}

func (s *Service) installAccess(ctx context.Context, r *jobs.Run, d *data) error {
	env := s.env(r.Log())
	// A key login may already work: this is a resume after the password was
	// erased, or after a crash between the key login and the erasure.
	switch c, err := s.connect(ctx, r, d.Server); {
	case err == nil:
		defer func() { _ = c.Close() }()
		if verr := remote.VerifyAccess(ctx, env, c); verr == nil {
			r.Log().Info("Access for %s is already in place", remote.DeployUser)
			return r.DeleteSecret(ctx, rootPasswordSecret)
		}
	case !errors.Is(err, sshx.ErrAuth):
		return err
	}

	pw, ok, _ := r.Secret(rootPasswordSecret)
	if !ok || pw == "" {
		return jobs.Permanent(errors.New("the root password is needed again; use Retry and enter it"))
	}
	line, _, err := s.SSH.PublicKey(ctx)
	if err != nil {
		return err
	}
	personal, err := s.SSH.PersonalKeys(ctx)
	if err != nil {
		return err
	}
	root, err := s.SSH.Connect(ctx, s.target(d.Server, "root", pw), r.Log())
	if err != nil {
		return rootErr(err)
	}
	defer func() { _ = root.Close() }()
	if err := remote.InstallAccess(ctx, env, root, append([]string{line}, personal...)); err != nil {
		return err
	}
	c, err := s.connect(ctx, r, d.Server)
	if err != nil {
		return fmt.Errorf("key login as %s failed after the keys were installed: %w", remote.DeployUser, err)
	}
	defer func() { _ = c.Close() }()
	if err := remote.VerifyAccess(ctx, env, c); err != nil {
		return err
	}
	// From here nothing can use or show the password.
	return r.DeleteSecret(ctx, rootPasswordSecret)
}

func (s *Service) baseBootstrap(ctx context.Context, r *jobs.Run, d *data) error {
	c, err := s.connect(ctx, r, d.Server)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	reconnect := func(ctx context.Context) (remote.Conn, error) {
		return s.connect(ctx, r, d.Server)
	}
	return remote.BaseBootstrap(ctx, s.env(r.Log()), c, reconnect, d.Server.SSHPort, d.Man.Ports)
}

func (s *Service) dnsStep(ctx context.Context, r *jobs.Run, d *data) error {
	srv := d.Server
	names := []string{srv.ManagementHostname}
	if srv.ProxyHostname != srv.ManagementHostname {
		names = append(names, srv.ProxyHostname)
	}
	drv := s.DNS()
	for _, name := range names {
		rec, err := drv.Ensure(ctx, name, srv.IP, srv.Name, d.Payload.OverwriteDNS)
		var conflict *dns.ConflictError
		if errors.As(err, &conflict) {
			return jobs.Permanent(fmt.Errorf("%s already points to %s and Proxier did not create that record; retry and choose Overwrite to replace it", conflict.Name, conflict.Content))
		}
		if err != nil {
			return err
		}
		err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			return store.UpsertDNSRecord(ctx, tx, store.DNSRecord{
				ServerID: srv.ID, Provider: rec.Provider, ZoneID: rec.ZoneID, Zone: rec.Zone, Name: rec.Name, Type: rec.Type,
				Content: rec.Content, RecordID: rec.RecordID,
			}, db.At(s.now()))
		})
		if err != nil {
			return err
		}
		r.Log().Info("DNS record %s → %s is in place (DNS only, TTL 60)", name, srv.IP)
	}
	w := s.Waiter()
	for _, name := range names {
		r.Log().Info("Waiting for resolvers to show %s", name)
		if err := w.Wait(ctx, name, srv.IP, r.Log()); err != nil {
			if errors.Is(err, dns.ErrNotVisible) {
				return fmt.Errorf("%s is not visible on every resolver: %w", name, err)
			}
			return err
		}
	}
	return nil
}

func (s *Service) generatedValues(ctx context.Context, r *jobs.Run, d *data) error {
	all, created, err := ensureGenerated(d)
	if err != nil {
		return err
	}
	if len(created) == 0 {
		r.Log().Info("All %d generated values exist already", len(d.Gen))
		return nil
	}
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, k := range created {
			if err := store.PutGenerated(ctx, tx, d.Server.ID, k, sealed.SealGen(s.Vault, d.Server.ID, k, all[k]), db.At(s.now())); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range created {
		r.Log().Redact(all[k])
	}
	r.Log().Info("Generated %d values: %s", len(created), strings.Join(created, ", "))
	return nil
}

func (s *Service) uploadFiles(ctx context.Context, r *jobs.Run, d *data) error {
	rendered, err := d.render()
	if err != nil {
		return err
	}
	srv := d.Server
	dep, err := s.provisionDeployment(ctx, r, d)
	if err != nil {
		return err
	}
	var previous []string
	if cur, ok, err := store.CurrentDeployment(ctx, s.DB.R, srv.ID); err != nil {
		return err
	} else if ok {
		rows, err := store.DeployedFiles(ctx, s.DB.R, cur.ID)
		if err != nil {
			return err
		}
		for _, f := range rows {
			previous = append(previous, f.Path)
		}
	}
	files := make([]remote.File, len(rendered.Files))
	for i, f := range rendered.Files {
		files[i] = remote.File{Path: f.Path, Mode: f.Mode, Content: f.Content}
	}
	c, err := s.connect(ctx, r, srv)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if _, err := remote.UploadFiles(ctx, s.env(r.Log()), c, d.Man.Dir, files, previous); err != nil {
		return err
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		rows := make([]store.DeployedFile, len(files))
		for i, f := range files {
			rows[i] = store.DeployedFile{DeploymentID: dep, Path: f.Path, Mode: int(f.Mode.Perm()), SHA256: sum(f.Content),
				Content: sealed.SealFile(s.Vault, dep, f.Path, f.Content)}
		}
		if err := store.ReplaceDeployedFiles(ctx, tx, dep, rows); err != nil {
			return err
		}
		return store.SetDeploymentUploaded(ctx, tx, dep, len(rows))
	})
}

// provisionDeployment returns the server's running provision deployment, made
// on the first run of this step; a retry reuses it, its files replaced.
func (s *Service) provisionDeployment(ctx context.Context, r *jobs.Run, d *data) (int64, error) {
	srv := d.Server
	if dep, ok, err := store.RunningDeployment(ctx, s.DB.R, srv.ID, "provision"); err != nil {
		return 0, err
	} else if ok {
		return dep.ID, nil
	}
	secret, err := sealed.OpenParams(s.Vault, srv.ID, srv.ParamsSecret)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, err = store.InsertDeployment(ctx, tx, store.Deployment{
			ServerID: srv.ID, Kind: "provision", TemplateVersion: srv.TemplateVersion, Params: srv.Params,
			JobID: nullInt(r.Info().ID),
		}, db.At(s.now()))
		if err != nil {
			return err
		}
		if blob := sealed.SealDeploymentSecrets(s.Vault, id, sealed.DeploymentSecrets{Params: secret, Gen: d.Gen}); blob != nil {
			return store.SetDeploymentParamsSecret(ctx, tx, id, blob)
		}
		return nil
	})
	return id, err
}

func (s *Service) install(ctx context.Context, r *jobs.Run, d *data) error {
	rendered, err := d.render()
	if err != nil {
		return err
	}
	var steps []manifest.Step
	for _, st := range rendered.Install {
		if st.Kind != "base-bootstrap" && st.Kind != "upload-files" { // job steps of their own
			steps = append(steps, st)
		}
	}
	c, err := s.connect(ctx, r, d.Server)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	for i, st := range steps {
		r.Log().Info("Template step %d of %d: %s", i+1, len(steps), st.Kind)
		if err := remote.RunStep(ctx, s.env(r.Log()), c, d.Man.Dir, st); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) endpointsStep(ctx context.Context, r *jobs.Run, d *data) error {
	rendered, err := d.render()
	if err != nil {
		return err
	}
	srv := d.Server
	rows, err := sealed.EndpointRows(s.Vault, srv.ID, rendered.Endpoints, flagOf(d.Loc.Country), d.Loc.Name, srv.Number)
	if err != nil {
		return err
	}
	for _, row := range rows {
		r.Log().Info("Endpoint %s: %s:%d (%s)", row.Key, row.Host, row.Port, row.DisplayName)
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return store.ReplaceEndpoints(ctx, tx, srv.ID, rows, db.At(s.now()))
	})
}

func (s *Service) selfCheck(ctx context.Context, r *jobs.Run, d *data) error {
	rendered, err := d.render()
	if err != nil {
		return err
	}
	c, err := s.connect(ctx, r, d.Server)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return remote.SelfCheckReport(ctx, s.env(r.Log()), c, d.Man.Dir, rendered.Checks, remote.DefaultThresholds)
}

// proxyOptions are the options of a proxy test from the settings and the
// template's own URL.
func (s *Service) proxyOptions(ctx context.Context, templateURL string) (proxy.Options, error) {
	url, timeout, stall, err := conf.ProxyTest(ctx, s.Settings, templateURL)
	if err != nil {
		return proxy.Options{}, err
	}
	return proxy.Options{URL: url, Timeout: timeout, Stall: stall}, nil
}

func (s *Service) smokeTest(ctx context.Context, r *jobs.Run, d *data) error {
	rendered, err := d.render()
	if err != nil {
		return err
	}
	rows, err := store.Endpoints(ctx, s.DB.R, d.Server.ID)
	if err != nil {
		return err
	}
	eps, err := sealed.OpenEndpoints(s.Vault, rows)
	if err != nil {
		return err
	}
	base, err := s.proxyOptions(ctx, rendered.ProxyTestURL)
	if err != nil {
		return err
	}
	timings := map[string]proxyTiming{}
	for _, e := range eps {
		opts := base
		if s.ProxyOptions != nil {
			opts = s.ProxyOptions(ctx, e, opts)
		}
		var last proxy.Result
		for attempt := 1; attempt <= s.SmokeAttempts; attempt++ {
			r.Log().Info("Proxy test through %s, attempt %d of %d", e.Key, attempt, s.SmokeAttempts)
			last = s.ProxyTest(ctx, e, opts)
			if last.OK {
				r.Log().Info("Proxy test passed: first byte after %d ms, %d kbit/s", last.FirstByteMS, last.ThroughputKbps)
				timings[e.Key] = proxyTiming{FirstByteMS: last.FirstByteMS, Kbps: last.ThroughputKbps}
				break
			}
			r.Log().Warn("Proxy test failed (%s): %s", last.Class, last.Error)
			if attempt < s.SmokeAttempts {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(s.SmokeGap):
				}
			}
		}
		if !last.OK {
			return fmt.Errorf("the proxy test through %s failed: %s: %s", e.Key, last.Class, last.Error)
		}
	}
	d.Payload.ProxyTest = timings
	return r.SavePayload(ctx, d.Payload)
}

func (s *Service) activate(ctx context.Context, r *jobs.Run, d *data) error {
	srv := d.Server
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return s.activateTx(ctx, tx, srv, r.Info().Actor(), map[string]any{"proxy_test": d.Payload.ProxyTest, "forced": false})
	})
}

// activateTx makes the server active, closes its provision deployment as
// succeeded and records server.activated.
func (s *Service) activateTx(ctx context.Context, tx *sqlx.Tx, srv store.Server, actor string, payload map[string]any) error {
	now := db.At(s.now())
	if err := store.MarkActive(ctx, tx, srv.ID, srv.TemplateVersion, now); err != nil {
		return err
	}
	if dep, ok, err := store.RunningDeployment(ctx, tx, srv.ID, "provision"); err != nil {
		return err
	} else if ok {
		if err := store.FinishDeployment(ctx, tx, dep.ID, "succeeded", "", now); err != nil {
			return err
		}
	} else if dep, ok, err := store.FailedDeployment(ctx, tx, srv.ID, "provision"); err != nil {
		return err
	} else if ok {
		// Activate anyway: the deployment the failure closed becomes the current one.
		if _, err := tx.ExecContext(ctx, `UPDATE servers_deployments SET state = 'succeeded', error = NULL WHERE id = ?`, dep.ID); err != nil {
			return err
		}
	}
	_, err := s.Events.Record(ctx, tx, events.Event{Type: "server.activated", Subject: store.ServerSubject(srv.ID), Actor: actor, Payload: payload})
	return err
}

// --- failure and cancellation

func (s *Service) onFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	step := ""
	_ = tx.GetContext(ctx, &step, `SELECT error_step FROM jobs WHERE id = ?`, j.ID)
	return s.stopped(ctx, tx, j, step, cause.Error(), false)
}

func (s *Service) onCancelled(ctx context.Context, tx *sqlx.Tx, j jobs.Info, by string) error {
	step := ""
	_ = tx.GetContext(ctx, &step, `SELECT name FROM job_steps WHERE job_id = ? AND state = 'cancelled' ORDER BY idx LIMIT 1`, j.ID)
	return s.stopped(ctx, tx, j, step, "cancelled", true)
}

// stopped puts the job's server into failed with the step and error, fails its
// running deployment and records the failure event.
func (s *Service) stopped(ctx context.Context, tx *sqlx.Tx, j jobs.Info, step, errText string, cancelled bool) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(j.ResourceKey, "server:"), 10, 64)
	if err != nil {
		return fmt.Errorf("job %d has no server: %w", j.ID, err)
	}
	if len(errText) > maxFailedError {
		errText = errText[:maxFailedError]
	}
	now := db.At(s.now())
	if err := store.MarkFailed(ctx, tx, id, step, errText); err != nil {
		return err
	}
	if err := store.FailRunningDeployments(ctx, tx, id, errText, now); err != nil {
		return err
	}
	payload := map[string]any{"step": step, "error": errText}
	if cancelled {
		payload["cancelled"] = true
	}
	_, err = s.Events.Record(ctx, tx, events.Event{Type: "server.provisioning_failed", Subject: store.ServerSubject(id), Actor: j.Actor(), Payload: payload})
	return err
}

// --- small helpers

func marshalStrings(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// secretMask stands for a secret in a preview made without Reveal.
const secretMask = "•••"

// PreviewContext builds the render context of a template draft for a real
// server: its address, names, parameters and generated values. Without reveal
// every generated value and secret parameter is shown as ••• so a preview on a
// shared screen leaks nothing. Generated values the draft declares and the
// server lacks are made on the spot, for the preview only.
func (s *Service) PreviewContext(ctx context.Context, serverID int64, reveal bool) (func(m *manifest.Manifest, slug string) (render.Context, error), error) {
	srv, err := store.GetServer(ctx, s.DB.R, serverID)
	if err != nil {
		return nil, err
	}
	loc, err := s.Store.Location(ctx, srv.LocationID)
	if err != nil {
		return nil, err
	}
	secret, err := sealed.OpenParams(s.Vault, srv.ID, srv.ParamsSecret)
	if err != nil {
		return nil, err
	}
	rows, err := store.GeneratedValues(ctx, s.DB.R, srv.ID)
	if err != nil {
		return nil, err
	}
	have, err := sealed.OpenGenerated(s.Vault, srv.ID, rows)
	if err != nil {
		return nil, err
	}
	public := store.ParseParams(srv.Params)
	return func(m *manifest.Manifest, slug string) (render.Context, error) {
		all, _, err := gen.Ensure(m.Generated, have)
		if err != nil {
			return render.Context{}, err
		}
		genValues := map[string]string{}
		for k, val := range all {
			genValues[k] = val
			if !reveal {
				genValues[k] = secretMask
			}
		}
		params := map[string]any{}
		for _, p := range m.Parameters {
			raw, ok := public[p.Key]
			if sv, isSecret := secret[p.Key]; isSecret {
				raw, ok = sv, true
				if !reveal {
					raw = secretMask
				}
			}
			if ok {
				params[p.Key] = render.ParamValue(p, raw)
			}
		}
		return render.Context{
			Server: render.Server{
				Name: srv.Name, Number: srv.Number, IP: srv.IP, SSHPort: srv.SSHPort,
				Location:           render.Location{Code: loc.Code, Name: loc.Name, Country: loc.Country},
				ManagementHostname: srv.ManagementHostname, ProxyHostname: srv.ProxyHostname,
			},
			Params: params, Gen: genValues, Template: render.Template{Slug: slug, Version: srv.TemplateVersion},
		}, nil
	}, nil
}

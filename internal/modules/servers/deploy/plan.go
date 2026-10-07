package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io/fs"
	"sort"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/gen"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// Kinds of change a plan can be for.
const (
	KindRedeploy = "redeploy"
	KindUpgrade  = "upgrade"
	KindParams   = "params"
)

// Target is what a plan renders: a version of the server's template with
// parameter values. Version 0 is the version in force. Params overlay the
// stored values by key; keys the version does not declare are ignored.
type Target struct {
	Kind    string // redeploy upgrade params; "" derives it from Version
	Version int
	Params  map[string]string
	Force   bool
}

// FileChange is one file that differs from the server's current files.
type FileChange struct {
	Path, Change     string // change: added changed removed
	ModeFrom, ModeTo fs.FileMode
	Diff             string // masked unified diff
	// SecretOnly: only a secret value differs, so the masked diff is empty.
	SecretOnly       bool
	OldSize, NewSize int
}

// EndpointChange is an endpoint of the target compared with the stored one.
type EndpointChange struct {
	Key, Change string // added removed changed
	URIChanged  bool
}

// Plan is what a redeploy would do, rendered for the server and compared with
// its current files. It never holds a secret value.
type Plan struct {
	ServerID               int64
	Kind                   string
	FromVersion, ToVersion int
	Force                  bool
	Files                  []FileChange
	// UploadCount is how many files the job uploads: the added and changed
	// ones, or all of them when forced.
	UploadCount   int
	NewGenerated  []string
	Endpoints     []EndpointChange
	ParamsAdded   []string // declared by the target, not by the version in force
	ParamsRemoved []string // declared by the version in force, not by the target
	ParamsChanged []string // declared by both, with another value
	MissingParams []string // required, no default, not given
	Steps         []string // job step names that will run
	Warnings      []Msg
	// Problems are the parameter values the version refuses, by form field
	// ("param.log_level"); with any, Apply changes nothing.
	Problems FieldErrors
	Empty    bool
	Hash     string
}

// RenderError is a version that does not render for the server.
type RenderError struct{ Message string }

func (e *RenderError) Error() string { return "rendering the template: " + e.Message }

// current is the server's current files: the latest succeeded deployment that
// uploaded any.
type current struct {
	Dep     store.Deployment
	Files   map[string]curFile
	Secrets sealed.DeploymentSecrets
}

type curFile struct {
	Mode    fs.FileMode
	Content []byte
}

// mix writes a line into the plan's hash (a hash.Hash never fails to write).
func mix(h hash.Hash, format string, args ...any) { _, _ = fmt.Fprintf(h, format, args...) }

func (s *Service) loadCurrent(ctx context.Context, serverID int64) (*current, error) {
	dep, ok, err := store.CurrentDeployment(ctx, s.DB.R, serverID)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := store.DeployedFiles(ctx, s.DB.R, dep.ID)
	if err != nil {
		return nil, err
	}
	c := &current{Dep: dep, Files: make(map[string]curFile, len(rows))}
	for _, r := range rows {
		b, err := sealed.OpenFile(s.Vault, dep.ID, r.Path, r.Content)
		if err != nil {
			return nil, err
		}
		c.Files[r.Path] = curFile{Mode: fs.FileMode(r.Mode), Content: b}
	}
	if c.Secrets, err = sealed.OpenDeploymentSecrets(s.Vault, dep.ID, dep.ParamsSecret); err != nil {
		return nil, err
	}
	return c, nil
}

// computed is a target rendered for a server, with everything a plan and the
// job steps that apply it need.
type computed struct {
	Srv    store.Server
	Loc    store.Location
	Slug   string
	Target Target

	Man      *manifest.Manifest
	CurMan   *manifest.Manifest // the version in force; nil when it no longer parses
	Public   map[string]string  // parameters to store: public
	Secret   map[string]string  // and secret
	Gen      map[string]string  // every generated value the target renders with, new ones included
	OldGen   map[string]string
	Created  []string // generated values the target adds
	Rendered *render.Rendered
	Eps      []endpoint.Endpoint
	Cur      *current
	// Upload is what the job puts on the server; Remove what it deletes.
	Upload []remote.File
	Remove []string

	Plan Plan
	// Errs are the parameter values the version refuses.
	Errs FieldErrors

	currentSecretParams map[string]string
	hash                hash.Hash
	filesChanged        int
}

// allParams is every parameter value, public and secret.
func (c *computed) allParams() map[string]string {
	out := map[string]string{}
	for k, v := range c.Public {
		out[k] = v
	}
	for k, v := range c.Secret {
		out[k] = v
	}
	return out
}

func (c *computed) renderContext() render.Context {
	params := map[string]any{}
	all := c.allParams()
	for _, p := range c.Man.Parameters {
		if raw, ok := all[p.Key]; ok {
			params[p.Key] = render.ParamValue(p, raw)
		}
	}
	return render.Context{
		Server: render.Server{
			Name: c.Srv.Name, Number: c.Srv.Number, IP: c.Srv.IP, SSHPort: c.Srv.SSHPort,
			Location:           render.Location{Code: c.Loc.Code, Name: c.Loc.Name, Country: c.Loc.Country},
			ManagementHostname: c.Srv.ManagementHostname, ProxyHostname: c.Srv.ProxyHostname,
		},
		Params: params, Gen: c.Gen, Template: render.Template{Slug: c.Slug, Version: c.Plan.ToVersion},
	}
}

// parseVersion loads a version's manifest.
func (s *Service) parseVersion(ctx context.Context, templateID int64, n int) (*manifest.Manifest, map[string][]byte, error) {
	v, err := s.Templates.Version(ctx, templateID, n)
	if err != nil {
		return nil, nil, fmt.Errorf("load template version %d: %w", n, err)
	}
	m, fs := manifest.Parse(v.Files[manifest.Name])
	if m == nil {
		return nil, nil, fmt.Errorf("the manifest of version %d does not parse: %v", n, fs)
	}
	return m, v.Files, nil
}

// compute renders the target for srv and compares it with the current files.
// genOverlay replaces generated values (a rotation's pending ones). It writes
// nothing; new generated values exist in memory only.
func (s *Service) compute(ctx context.Context, srv store.Server, t Target, genOverlay map[string]string) (*computed, error) {
	loc, err := s.Store.Location(ctx, srv.LocationID)
	if err != nil {
		return nil, err
	}
	tpl, err := store.GetTemplate(ctx, s.DB.R, srv.TemplateID)
	if err != nil {
		return nil, err
	}
	if t.Version == 0 {
		t.Version = srv.TemplateVersion
	}
	if t.Kind == "" {
		t.Kind = KindRedeploy
		if t.Version != srv.TemplateVersion {
			t.Kind = KindUpgrade
		}
	}
	c := &computed{Srv: srv, Loc: loc, Slug: tpl.Slug, Target: t, Errs: FieldErrors{}}
	c.Plan = Plan{ServerID: srv.ID, Kind: t.Kind, FromVersion: srv.TemplateVersion, ToVersion: t.Version, Force: t.Force}

	var tplFiles map[string][]byte
	if c.Man, tplFiles, err = s.parseVersion(ctx, srv.TemplateID, t.Version); err != nil {
		return nil, err
	}
	if t.Version == srv.TemplateVersion {
		c.CurMan = c.Man
	} else if m, _, err := s.parseVersion(ctx, srv.TemplateID, srv.TemplateVersion); err == nil {
		c.CurMan = m
	}

	if err := s.computeParams(ctx, c); err != nil {
		return nil, err
	}

	rows, err := store.GeneratedValues(ctx, s.DB.R, srv.ID)
	if err != nil {
		return nil, err
	}
	if c.OldGen, err = sealed.OpenGenerated(s.Vault, srv.ID, rows); err != nil {
		return nil, err
	}
	if c.Gen, c.Created, err = gen.Ensure(c.Man.Generated, c.OldGen); err != nil {
		return nil, err
	}
	for k, v := range genOverlay {
		c.Gen[k] = v
	}
	c.Plan.NewGenerated = append([]string(nil), c.Created...)

	if c.Cur, err = s.loadCurrent(ctx, srv.ID); err != nil {
		return nil, err
	}

	var findings []string
	var fatal bool
	rendered, fs := render.Render(c.Man, tplFiles, c.renderContext())
	for _, f := range fs {
		if f.Severity == "error" {
			findings = append(findings, f.String())
			fatal = true
		}
	}
	if fatal {
		return nil, &RenderError{Message: strings.Join(findings, "; ")}
	}
	c.Rendered = rendered

	if err := s.diffFiles(c); err != nil {
		return nil, err
	}
	if err := s.diffEndpoints(ctx, c); err != nil {
		return nil, err
	}
	s.finishPlan(c)
	return c, nil
}

// computeParams applies the given values over the stored ones, fills defaults
// and checks each declared parameter. Values of parameters the target does not
// declare stay in storage as they were.
func (s *Service) computeParams(ctx context.Context, c *computed) error {
	srv, t := c.Srv, c.Target
	secretStored, err := sealed.OpenParams(s.Vault, srv.ID, srv.ParamsSecret)
	if err != nil {
		return err
	}
	stored := store.ParseParams(srv.Params)
	for k, v := range secretStored {
		stored[k] = v
	}
	declared := map[string]manifest.Parameter{}
	for _, p := range c.Man.Parameters {
		declared[p.Key] = p
	}
	oldDeclared := map[string]bool{}
	if c.CurMan != nil {
		for _, p := range c.CurMan.Parameters {
			oldDeclared[p.Key] = true
		}
	}

	c.Public, c.Secret = map[string]string{}, map[string]string{}
	// What the version no longer declares is kept as it is.
	for k, v := range stored {
		if _, ok := declared[k]; ok {
			continue
		}
		if _, sec := secretStored[k]; sec {
			c.Secret[k] = v
		} else {
			c.Public[k] = v
		}
	}
	for _, p := range c.Man.Parameters {
		raw, given := t.Params[p.Key]
		if !given || p.Secret && strings.TrimSpace(raw) == "" && stored[p.Key] != "" {
			// A secret left blank in a form keeps the value it has.
			raw = stored[p.Key]
		}
		raw = strings.TrimSpace(raw)
		if raw == "" && p.Default != nil {
			raw = *p.Default
		}
		_, hadBefore := stored[p.Key]
		if !oldDeclared[p.Key] && !hadBefore {
			c.Plan.ParamsAdded = append(c.Plan.ParamsAdded, p.Key)
		}
		if raw == "" {
			if p.Required {
				c.Plan.MissingParams = append(c.Plan.MissingParams, p.Key)
				c.Errs["param."+p.Key] = Msg{Key: "servers.err.param_required", Args: i18n.Args{"label": p.Label}}
			}
			continue
		}
		if key := render.ParamProblem(p, raw); key != "" {
			c.Errs["param."+p.Key] = Msg{Key: key, Args: i18n.Args{"label": p.Label}}
			continue
		}
		if p.Secret {
			c.Secret[p.Key] = raw
		} else {
			c.Public[p.Key] = raw
		}
		if oldDeclared[p.Key] && c.oldValue(p.Key, stored) != raw {
			c.Plan.ParamsChanged = append(c.Plan.ParamsChanged, p.Key)
		}
	}
	for k := range oldDeclared {
		if _, ok := declared[k]; !ok {
			c.Plan.ParamsRemoved = append(c.Plan.ParamsRemoved, k)
		}
	}
	sort.Strings(c.Plan.ParamsAdded)
	sort.Strings(c.Plan.ParamsRemoved)
	sort.Strings(c.Plan.ParamsChanged)
	sort.Strings(c.Plan.MissingParams)
	return nil
}

// oldValue is what the version in force rendered the parameter with: the
// stored value, else its default.
func (c *computed) oldValue(key string, stored map[string]string) string {
	if v := strings.TrimSpace(stored[key]); v != "" {
		return v
	}
	if c.CurMan != nil {
		for _, p := range c.CurMan.Parameters {
			if p.Key == key && p.Default != nil {
				return *p.Default
			}
		}
	}
	return ""
}

// secrets returns the values to hide on the side of the current files and on
// the side of the target.
func (c *computed) secrets() (old, new Secrets) {
	old = Secrets{}
	old.AddAll(c.OldGen)
	old.AddAll(c.currentSecretParams)
	if c.Cur != nil {
		old.AddAll(c.Cur.Secrets.Gen)
		old.AddAll(c.Cur.Secrets.Params)
	}
	new = Secrets{}
	new.AddAll(c.Gen)
	new.AddAll(c.Secret)
	return old, new
}

// DiffMaskers returns the maskers for comparing two deployments of a server:
// each hides everything that may be in its files (the server's values now and
// the deployment's own), and a value that only one side was rendered with
// says "(old)" or "(new)", so a changed secret still shows as a change.
func (s *Service) DiffMaskers(ctx context.Context, srv store.Server, prev, cur store.Deployment) (oldMask, newMask Masker, err error) {
	now, err := s.StoredSecrets(ctx, srv, nil)
	if err != nil {
		return oldMask, newMask, err
	}
	own := func(d store.Deployment) (Secrets, error) {
		ds, err := sealed.OpenDeploymentSecrets(s.Vault, d.ID, d.ParamsSecret)
		if err != nil {
			return nil, err
		}
		out := Secrets{}
		out.AddAll(ds.Gen)
		out.AddAll(ds.Params)
		return out, nil
	}
	a, err := own(prev)
	if err != nil {
		return oldMask, newMask, err
	}
	b, err := own(cur)
	if err != nil {
		return oldMask, newMask, err
	}
	withNow := func(own Secrets) Secrets {
		out := Secrets{}
		for k, vals := range own {
			for _, v := range vals {
				out.Add(k, v)
			}
		}
		for k, vals := range now {
			for _, v := range vals {
				out.Add(k, v)
			}
		}
		return out
	}
	return NewMasker(withNow(a), b, "old"), NewMasker(withNow(b), a, "new"), nil
}

// StoredSecrets returns every secret value that may be in a deployment's
// files: the server's generated values and secret parameters now, and those
// the deployment's own files were rendered with.
func (s *Service) StoredSecrets(ctx context.Context, srv store.Server, dep *store.Deployment) (Secrets, error) {
	out := Secrets{}
	rows, err := store.GeneratedValues(ctx, s.DB.R, srv.ID)
	if err != nil {
		return nil, err
	}
	gens, err := sealed.OpenGenerated(s.Vault, srv.ID, rows)
	if err != nil {
		return nil, err
	}
	out.AddAll(gens)
	if pend, err := sealed.OpenPending(s.Vault, srv.ID, rows); err == nil {
		out.AddAll(pend)
	}
	params, err := sealed.OpenParams(s.Vault, srv.ID, srv.ParamsSecret)
	if err != nil {
		return nil, err
	}
	out.AddAll(params)
	if dep != nil {
		ds, err := sealed.OpenDeploymentSecrets(s.Vault, dep.ID, dep.ParamsSecret)
		if err != nil {
			return nil, err
		}
		out.AddAll(ds.Gen)
		out.AddAll(ds.Params)
	}
	return out, nil
}

// diffFiles compares the rendered files with the current ones.
func (s *Service) diffFiles(c *computed) error {
	srvSecret, err := sealed.OpenParams(s.Vault, c.Srv.ID, c.Srv.ParamsSecret)
	if err != nil {
		return err
	}
	c.currentSecretParams = srvSecret
	oldSecrets, newSecrets := c.secrets()
	oldMask := NewMasker(oldSecrets, newSecrets, "old")
	newMask := NewMasker(newSecrets, oldSecrets, "new")

	var curFiles map[string]curFile
	if c.Cur != nil {
		curFiles = c.Cur.Files
	}
	rendered := map[string]render.RenderedFile{}
	for _, f := range c.Rendered.Files {
		rendered[f.Path] = f
	}

	paths := make([]string, 0, len(rendered))
	for p := range rendered {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	hash := sha256.New()
	var changed int
	for _, p := range paths {
		f := rendered[p]
		old, had := curFiles[p]
		change := ""
		switch {
		case !had:
			change = templates.Added
		case !bytes.Equal(old.Content, f.Content) || old.Mode.Perm() != f.Mode.Perm():
			change = templates.Changed
		}
		if c.Target.Force || change != "" {
			c.Upload = append(c.Upload, remote.File{Path: p, Mode: f.Mode, Content: f.Content})
		}
		if change == "" {
			continue
		}
		changed++
		fc := FileChange{Path: p, Change: change, ModeFrom: old.Mode.Perm(), ModeTo: f.Mode.Perm(), OldSize: len(old.Content), NewSize: len(f.Content)}
		oldMasked, newMasked := oldMask.Apply(old.Content), newMask.Apply(f.Content)
		if !had {
			oldMasked = nil
		}
		for _, d := range templates.Diff(map[string][]byte{p: oldMasked}, map[string][]byte{p: newMasked}) {
			if !had {
				// templates.Diff reads an empty old file as a changed file; this one is new.
				d.Change = templates.Added
			}
			fc.Diff = d.Unified
			if had && d.Change == templates.Unchanged && !bytes.Equal(old.Content, f.Content) {
				fc.SecretOnly = true
			}
		}
		c.Plan.Files = append(c.Plan.Files, fc)
		mix(hash, "file %s %s %o>%o %x\n", p, change, old.Mode.Perm(), f.Mode.Perm(), sha256.Sum256(newMasked))
	}
	var gone []string
	for p := range curFiles {
		if _, ok := rendered[p]; !ok {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		old := curFiles[p]
		c.Remove = append(c.Remove, p)
		changed++
		fc := FileChange{Path: p, Change: templates.Removed, ModeFrom: old.Mode.Perm(), OldSize: len(old.Content)}
		for _, d := range templates.Diff(map[string][]byte{p: oldMask.Apply(old.Content)}, nil) {
			fc.Diff = d.Unified
		}
		c.Plan.Files = append(c.Plan.Files, fc)
		mix(hash, "removed %s\n", p)
	}
	sort.Slice(c.Plan.Files, func(i, j int) bool { return c.Plan.Files[i].Path < c.Plan.Files[j].Path })
	c.Plan.UploadCount = len(c.Upload)
	c.hash = hash
	c.filesChanged = changed
	return nil
}

// diffEndpoints compares the target's endpoints with the stored ones.
func (s *Service) diffEndpoints(ctx context.Context, c *computed) error {
	eps, err := sealed.Endpoints(c.Rendered.Endpoints, country.Flag(c.Loc.Country), c.Loc.Name, c.Srv.Number)
	if err != nil {
		return err
	}
	c.Eps = eps
	rows, err := store.Endpoints(ctx, s.DB.R, c.Srv.ID)
	if err != nil {
		return err
	}
	stored, err := sealed.OpenEndpoints(s.Vault, rows)
	if err != nil {
		return err
	}
	old := map[string]endpoint.Endpoint{}
	for _, e := range stored {
		old[e.Key] = e
	}
	seen := map[string]bool{}
	for _, e := range eps {
		seen[e.Key] = true
		o, ok := old[e.Key]
		if !ok {
			c.Plan.Endpoints = append(c.Plan.Endpoints, EndpointChange{Key: e.Key, Change: "added"})
			continue
		}
		if endpointsEqual(o, e) {
			continue
		}
		ch := EndpointChange{Key: e.Key, Change: "changed"}
		if t1, ok := endpoint.Lookup(o.Type); ok {
			if t2, ok := endpoint.Lookup(e.Type); ok {
				ch.URIChanged = t1.URI(o) != t2.URI(e)
			}
		}
		c.Plan.Endpoints = append(c.Plan.Endpoints, ch)
	}
	var removed []string
	for k := range old {
		if !seen[k] {
			removed = append(removed, k)
		}
	}
	sort.Strings(removed)
	for _, k := range removed {
		c.Plan.Endpoints = append(c.Plan.Endpoints, EndpointChange{Key: k, Change: "removed"})
		c.Plan.Warnings = append(c.Plan.Warnings, Msg{Key: "servers.plan.warn.endpoint_removed", Args: i18n.Args{"key": k}})
	}
	return nil
}

func endpointsEqual(a, b endpoint.Endpoint) bool {
	if a.Type != b.Type || a.Host != b.Host || a.Port != b.Port || a.Credential != b.Credential || a.DisplayName != b.DisplayName || len(a.Params) != len(b.Params) {
		return false
	}
	for k, v := range a.Params {
		if bv, ok := b.Params[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// finishPlan sets the steps, the warnings, Empty and the hash.
func (s *Service) finishPlan(c *computed) {
	p := &c.Plan
	if p.ToVersion < p.FromVersion {
		p.Warnings = append(p.Warnings, Msg{Key: "servers.plan.warn.downgrade", Args: i18n.Args{"from": p.FromVersion, "to": p.ToVersion}})
	}
	p.Empty = !p.Force && len(p.Files) == 0 && len(p.Endpoints) == 0 && p.FromVersion == p.ToVersion &&
		len(p.ParamsChanged) == 0 && len(p.ParamsAdded) == 0 && len(p.ParamsRemoved) == 0 && len(p.NewGenerated) == 0
	p.Steps = []string{StepConnect}
	if len(p.NewGenerated) > 0 {
		p.Steps = append(p.Steps, StepGenerated)
	}
	p.Steps = append(p.Steps, StepUpload, StepRedeploy, StepEndpoints, StepSelfCheck, StepProxyTest, StepRecord)

	h := c.hash
	mix(h, "server %d %s %d>%d force=%v\n", p.ServerID, p.Kind, p.FromVersion, p.ToVersion, p.Force)
	mix(h, "new %s\n", strings.Join(p.NewGenerated, ","))
	for _, e := range p.Endpoints {
		mix(h, "endpoint %s %s %v\n", e.Key, e.Change, e.URIChanged)
	}
	mix(h, "params +%s -%s ~%s\n", strings.Join(p.ParamsAdded, ","), strings.Join(p.ParamsRemoved, ","), strings.Join(p.ParamsChanged, ","))
	p.Hash = hex.EncodeToString(h.Sum(nil))
	if len(c.Errs) > 0 {
		p.Problems = c.Errs
	}
}

// Plan renders the target for the server and compares it with the server's
// current files. It writes nothing. A server that is not active has no plan.
func (s *Service) Plan(ctx context.Context, serverID int64, t Target) (Plan, error) {
	c, err := s.computeFor(ctx, serverID, t)
	if err != nil {
		return Plan{}, err
	}
	return c.Plan, nil
}

func (s *Service) computeFor(ctx context.Context, serverID int64, t Target) (*computed, error) {
	srv, err := s.activeServer(ctx, s.DB.R, serverID)
	if err != nil {
		return nil, err
	}
	return s.compute(ctx, srv, t, nil)
}

// RollBack returns the target that undoes the server's latest failed
// deployment: the version and parameters of the current files, forced, because
// the failed deployment may have changed the server in ways the stored
// current files do not show. Only the latest deployment, and only a failed one
// that put files on the server, can be rolled back.
func (s *Service) RollBack(ctx context.Context, serverID int64) (Target, error) {
	srv, err := s.activeServer(ctx, s.DB.R, serverID)
	if err != nil {
		return Target{}, err
	}
	latest, ok, err := store.LatestDeployment(ctx, s.DB.R, serverID)
	if err != nil {
		return Target{}, err
	}
	if !ok || latest.State != "failed" || !latest.Uploaded {
		return Target{}, ErrNoRollBack
	}
	cur, err := s.loadCurrent(ctx, serverID)
	if err != nil {
		return Target{}, err
	}
	if cur == nil {
		return Target{}, ErrNoRollBack
	}
	params := store.ParseParams(cur.Dep.Params)
	for k, v := range cur.Secrets.Params {
		params[k] = v
	}
	t := Target{Kind: KindRedeploy, Version: cur.Dep.TemplateVersion, Params: params, Force: true}
	if t.Version != srv.TemplateVersion {
		t.Kind = KindUpgrade
	}
	return t, nil
}

// canRollBack reports whether RollBack would answer.
func (s *Service) CanRollBack(ctx context.Context, serverID int64) bool {
	_, err := s.RollBack(ctx, serverID)
	return err == nil
}

// summarize says in a log line what a plan holds.
func (p Plan) summarize() string {
	var added, changed, removed int
	for _, f := range p.Files {
		switch f.Change {
		case templates.Added:
			added++
		case templates.Changed:
			changed++
		case templates.Removed:
			removed++
		}
	}
	return fmt.Sprintf("%d files added, %d changed, %d removed; %d new generated values; %d endpoint changes", added, changed, removed, len(p.NewGenerated), len(p.Endpoints))
}

func marshalParams(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

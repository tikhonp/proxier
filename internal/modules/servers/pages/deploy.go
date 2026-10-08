package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// jobPrefix starts the names of the module's job types.
const jobPrefix = "servers" + "."

func (h *handler) registerDeploy(r web.Routes) {
	r.Admin.GET("/servers/:id/redeploy", h.redeployPlan, h.operable)
	r.Admin.GET("/servers/:id/upgrade", h.upgradePlan, h.operable)
	r.Admin.GET("/servers/:id/params", h.paramsPlan, h.operable)
	r.Admin.POST("/servers/:id/plan", h.planPost, h.operable)
	r.Admin.POST("/servers/:id/apply", h.applyPost, h.operable)
	r.Admin.GET("/servers/:id/rollback", h.rollbackPlan, h.operable)
	r.Admin.POST("/servers/:id/restart", h.opRestart, h.operable)
	r.Admin.POST("/servers/:id/images", h.opImages, h.operable)
	r.Admin.POST("/servers/:id/reboot", h.opReboot, h.operable)
	r.Admin.POST("/servers/:id/logs", h.opLogs, h.operable)
	r.Admin.GET("/servers/:id/rotate", h.rotatePage, h.operable)
	r.Admin.POST("/servers/:id/rotate", h.rotatePost, h.operable)
	h.registerStack(r)
	h.registerRollout(r)
}

// ----------------------------------------------------------- the server page

// changeView fills what the server page shows about changes to a running
// server: the actions, "update available", the roll back notice and the job of
// a change in progress (or the one the page was opened for).
func (h *handler) changeView(ctx context.Context, s store.Server, jobParam int64, v *serverView) error {
	if h.Deploy == nil || s.Retiring() || s.State == "retired" {
		return nil
	}
	switch s.State {
	case "active":
	case "failed":
		if ok, err := store.HasUploadedDeployment(ctx, h.Store.DB.R, s.ID); err != nil {
			return err
		} else if ok {
			v.Actions = []ui.Action{h.logsAction(ctx, s)}
		}
		return nil
	default:
		return nil
	}
	info, err := h.Templates.Get(ctx, s.TemplateID)
	if err != nil {
		return err
	}
	if info.DefaultVersion > s.TemplateVersion {
		v.UpdateTo = info.DefaultVersion
	}
	canRoll := h.Deploy.CanRollBack(ctx, s.ID)
	v.Actions = h.serverActions(ctx, s, canRoll, info.Latest > 1, v.UpdateTo)
	if canRoll {
		if d, ok, err := store.LatestDeployment(ctx, h.Store.DB.R, s.ID); err != nil {
			return err
		} else if ok {
			v.Rollback = &rollbackBand{Kind: d.Kind, Error: d.Error}
			if d.JobID.Valid {
				v.Rollback.JobHref = "/jobs/" + i64(d.JobID.Int64)
			}
		}
	}

	id := jobParam
	if id == 0 {
		list, err := h.Jobs.List(ctx, jobs.Filter{
			Subject: store.ServerSubject(s.ID), States: []jobs.State{jobs.Queued, jobs.Running, jobs.Interrupted}, Limit: 5,
		})
		if err != nil {
			return err
		}
		for _, j := range list {
			if j.Type != provision.JobType && strings.HasPrefix(j.Type, jobPrefix) {
				id = j.ID
				break
			}
		}
	}
	if id == 0 {
		return nil
	}
	j, err := h.Jobs.Job(ctx, id)
	if err != nil || j.Subject != store.ServerSubject(s.ID) || j.Type == provision.JobType || !strings.HasPrefix(j.Type, jobPrefix) {
		return nil // a stale or foreign id: the page just does not show it
	}
	v.Change, err = h.jobSide(ctx, id)
	return err
}

func (h *handler) logsAction(ctx context.Context, s store.Server) ui.Action {
	return ui.Action{ID: "server.logs", Label: "servers.act.logs", Method: "POST", Href: serverHref(s.ID) + "/logs"}
}

func (h *handler) serverActions(ctx context.Context, s store.Server, canRoll, otherVersions bool, updateTo int) []ui.Action {
	href := serverHref(s.ID)
	var acts []ui.Action
	if canRoll {
		acts = append(acts, ui.Action{ID: "server.rollback", Label: "servers.act.rollback", Method: "GET", Href: href + "/rollback", Kind: ui.Primary})
	}
	if otherVersions {
		label := "servers.act.upgrade"
		if updateTo > 0 {
			label = "servers.act.upgrade_available"
		}
		acts = append(acts, ui.Action{ID: "server.upgrade", Label: label, Method: "GET", Href: href + "/upgrade"})
	}
	acts = append(acts,
		ui.Action{ID: "server.params", Label: "servers.act.params", Method: "GET", Href: href + "/params"},
		ui.Action{ID: "server.rotate", Label: "servers.act.rotate", Method: "GET", Href: href + "/rotate"},
		ui.Action{
			ID: "server.restart", Label: "servers.act.restart", Method: "POST", Href: href + "/restart",
			Confirm: &ui.Confirm{Strength: 1, Title: i18n.T(ctx, "servers.act.restart"), Body: i18n.T(ctx, "servers.restart.body", i18n.Args{"name": s.Name})},
		},
		ui.Action{
			ID: "server.images", Label: "servers.act.images", Method: "POST", Href: href + "/images",
			Confirm: &ui.Confirm{Strength: 1, Title: i18n.T(ctx, "servers.act.images"), Body: i18n.T(ctx, "servers.images.body", i18n.Args{"name": s.Name})},
		},
		ui.Action{
			ID: "server.reboot", Label: "servers.act.reboot", Method: "POST", Href: href + "/reboot",
			Confirm: &ui.Confirm{
				Strength: 2, Title: i18n.T(ctx, "servers.reboot.title", i18n.Args{"name": s.Name}), Body: i18n.T(ctx, "servers.reboot.body"),
				Consequences: []string{i18n.T(ctx, "servers.reboot.c1"), i18n.T(ctx, "servers.reboot.c2")},
			},
		},
		h.logsAction(ctx, s),
	)
	return acts
}

// ------------------------------------------------------------------- plans

type endpointChangeView struct {
	Key, Text string
	Warn      bool
}

type planView struct {
	S    store.Server
	Flag string
	Kind string // redeploy upgrade params rollback
	// Title and Note head the page.
	Title, Note string
	Plan        *deploy.Plan
	Err         string // translated: the target does not render
	Summary     []string
	Diff        ui.DiffView
	// The target form of an upgrade or a parameter edit.
	Form     bool
	Versions []verOption
	Fields   []paramField
	Version  int
	// What the plan found, translated.
	ParamsAdded, ParamsRemoved, ParamsChanged []string
	NewGenerated                              []string
	Endpoints                                 []endpointChangeView
	Warnings                                  []string
	Steps                                     []string
	Missing                                   bool
	// Apply and Force are the buttons; the plan is applied by the hash it showed.
	Apply, Force *ui.Action
	At           string
}

// parseTarget reads the target a plan form or an apply posts.
func parseTarget(c *echo.Context, kind string) deploy.Target {
	t := deploy.Target{Kind: kind, Params: map[string]string{}}
	t.Version, _ = strconv.Atoi(c.FormValue("version"))
	t.Force = c.FormValue("force") == "1"
	if form, err := c.FormValues(); err == nil {
		for k, vs := range form {
			if key, ok := strings.CutPrefix(k, "param."); ok && len(vs) > 0 {
				t.Params[key] = vs[0]
			}
		}
	}
	return t
}

// activeServer loads a server for a change; anything not active is sent back
// to its page.
func (h *handler) activeServer(c *echo.Context) (store.Server, error) {
	s, err := h.loadServer(c)
	if err != nil {
		return s, err
	}
	if s.State != "active" || s.Retiring() {
		return s, echo.NewHTTPError(http.StatusConflict, "the server is not active")
	}
	return s, nil
}

func (h *handler) redeployPlan(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	t := deploy.Target{Kind: deploy.KindRedeploy, Force: c.QueryParam("force") == "1"}
	return h.renderPlan(c, http.StatusOK, s, "redeploy", t, nil)
}

func (h *handler) paramsPlan(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	return h.renderPlan(c, http.StatusOK, s, "params", deploy.Target{Kind: deploy.KindParams}, nil)
}

func (h *handler) upgradePlan(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	t := deploy.Target{Kind: deploy.KindUpgrade}
	t.Version, _ = strconv.Atoi(c.QueryParam("version"))
	if t.Version == 0 {
		info, err := h.Templates.Get(ctx, s.TemplateID)
		if err != nil {
			return err
		}
		t.Version = info.DefaultVersion
	}
	return h.renderPlan(c, http.StatusOK, s, "upgrade", t, nil)
}

func (h *handler) rollbackPlan(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	t, err := h.Deploy.RollBack(c.Request().Context(), s.ID)
	if errors.Is(err, deploy.ErrNoRollBack) {
		return web.Redirect(c, serverHref(s.ID))
	}
	if err != nil {
		return err
	}
	return h.renderPlan(c, http.StatusOK, s, "rollback", t, nil)
}

func (h *handler) planPost(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	kind := c.FormValue("kind")
	if kind != "upgrade" && kind != "params" && kind != "redeploy" && kind != "rollback" {
		kind = "redeploy"
	}
	return h.renderPlan(c, http.StatusOK, s, kind, parseTarget(c, formKind(kind)), nil)
}

// formKind is the deploy kind of a page kind (a roll back is a redeploy).
func formKind(kind string) string {
	if kind == "rollback" {
		return deploy.KindRedeploy
	}
	return kind
}

func (h *handler) applyPost(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	kind := c.FormValue("kind")
	t := parseTarget(c, formKind(kind))
	job, err := h.Deploy.Apply(c.Request().Context(), s.ID, t, c.FormValue("hash"), "admin")
	var fe deploy.FieldErrors
	switch {
	case errors.As(err, &fe):
		return h.renderPlan(c, http.StatusUnprocessableEntity, s, kind, t, fe)
	case errors.Is(err, deploy.ErrNothingToChange):
		return h.renderPlan(c, http.StatusOK, s, kind, t, nil)
	case errors.Is(err, deploy.ErrNotActive):
		return web.Redirect(c, serverHref(s.ID))
	case err != nil:
		var re *deploy.RenderError
		if errors.As(err, &re) {
			return h.renderPlan(c, http.StatusUnprocessableEntity, s, kind, t, nil)
		}
		return err
	}
	return web.Redirect(c, serverHref(s.ID)+"?job="+i64(job))
}

// renderPlan computes the plan of the target and draws the plan screen.
func (h *handler) renderPlan(c *echo.Context, status int, s store.Server, kind string, t deploy.Target, refused deploy.FieldErrors) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	h.noStore(c)
	v := planView{S: s, Flag: flagOf(s), Kind: kind, At: loc.Clock(h.Deploy.Now())}
	v.Form = kind == "upgrade" || kind == "params"

	plan, err := h.Deploy.Plan(ctx, s.ID, t)
	var re *deploy.RenderError
	switch {
	case errors.Is(err, deploy.ErrNotActive):
		return web.Redirect(c, serverHref(s.ID))
	case errors.As(err, &re):
		v.Err = i18n.T(ctx, "servers.plan.render_error", i18n.Args{"error": re.Message})
		status = http.StatusUnprocessableEntity
	case err != nil:
		return notFound(err)
	default:
		v.Plan = &plan
	}
	version := t.Version
	if version == 0 {
		version = s.TemplateVersion
	}
	v.Version = version
	if v.Plan != nil {
		version = plan.ToVersion
		v.Version = version
	}
	vn := vlabel(version)
	switch kind {
	case "upgrade":
		v.Title = i18n.T(ctx, "servers.plan.upgrade.title", i18n.Args{"name": s.Name, "from": vlabel(s.TemplateVersion), "to": vn})
	case "params":
		v.Title = i18n.T(ctx, "servers.plan.params.title", i18n.Args{"name": s.Name})
	case "rollback":
		v.Title = i18n.T(ctx, "servers.plan.rollback.title", i18n.Args{"name": s.Name, "to": vn})
	default:
		v.Title = i18n.T(ctx, "servers.plan.redeploy.title", i18n.Args{"name": s.Name})
	}
	v.Note = i18n.T(ctx, "servers.plan.note", i18n.Args{"name": s.Name, "time": v.At})

	var errs map[string]deploy.Msg
	if v.Plan != nil {
		errs = v.Plan.Problems
	}
	if refused != nil {
		errs = refused
	}
	if v.Form {
		if err := h.planForm(ctx, &v, t, errs); err != nil {
			return err
		}
	}
	if v.Plan != nil {
		h.fillPlan(ctx, &v, t)
	}
	return web.Render(c, status, planPage(h.shell(c, v.Title, "/servers"), v))
}

func flagOf(s store.Server) string { return countryFlag(s.Country) }

// planForm builds the version picker and the parameter fields of the target.
func (h *handler) planForm(ctx context.Context, v *planView, t deploy.Target, errs map[string]deploy.Msg) error {
	s := v.S
	if v.Kind == "upgrade" {
		vers, err := h.Templates.Versions(ctx, s.TemplateID)
		if err != nil {
			return err
		}
		for _, ver := range vers {
			label := vlabel(ver.Number)
			if ver.Default {
				label += " · " + i18n.T(ctx, "servers.new.default")
			}
			if ver.Number == s.TemplateVersion {
				label += " · " + i18n.T(ctx, "servers.plan.current")
			}
			v.Versions = append(v.Versions, verOption{Number: ver.Number, Label: label, Selected: ver.Number == v.Version})
		}
	}
	ver, err := h.Templates.Version(ctx, s.TemplateID, v.Version)
	if err != nil {
		return notFound(err)
	}
	m, fs := manifest.Parse(ver.Files[manifest.Name])
	if m == nil || !(finding.Report{Findings: fs}).OK() {
		return nil
	}
	stored := store.ParseParams(s.Params)
	secret, err := sealed.OpenParams(h.Vault, s.ID, s.ParamsSecret)
	if err != nil {
		return err
	}
	for _, p := range m.Parameters {
		pf := paramField{Name: "param." + p.Key, Label: p.Label, Type: p.Type, Help: p.Help, Required: p.Required, Secret: p.Secret, Options: p.Options}
		given, wasGiven := t.Params[p.Key]
		switch {
		case p.Secret:
			// A secret is never sent back to the page; blank keeps it.
			if _, has := secret[p.Key]; has {
				pf.Help = i18n.T(ctx, "servers.plan.secret_kept")
			}
		case wasGiven:
			pf.Value = given
		case stored[p.Key] != "":
			pf.Value = stored[p.Key]
		case p.Default != nil:
			pf.Value = *p.Default
		}
		if msg, ok := errs["param."+p.Key]; ok {
			pf.Err = i18n.T(ctx, msg.Key, msg.Args)
		}
		v.Fields = append(v.Fields, pf)
	}
	return nil
}

// fillPlan translates a plan into what the screen lists.
func (h *handler) fillPlan(ctx context.Context, v *planView, t deploy.Target) {
	p := v.Plan
	loc := i18n.From(ctx)
	var added, changed, removed int
	v.Diff = ui.DiffView{}
	for _, f := range p.Files {
		switch f.Change {
		case "added":
			added++
		case "changed":
			changed++
		case "removed":
			removed++
		}
		df := ui.DiffFile{Path: f.Path, Change: f.Change, Hunks: ui.ParseUnified(f.Diff)}
		switch {
		case f.SecretOnly:
			df.Note = i18n.T(ctx, "servers.plan.secret_only")
		case f.Change == "changed" && f.ModeFrom != f.ModeTo:
			df.Note = i18n.T(ctx, "servers.plan.mode", i18n.Args{"from": fmt.Sprintf("%04o", f.ModeFrom), "to": fmt.Sprintf("%04o", f.ModeTo)})
		}
		v.Diff.Files = append(v.Diff.Files, df)
	}
	if n := len(p.Files); n > 0 {
		v.Summary = append(v.Summary, loc.N("servers.plan.sum.files", int64(n)))
	}
	if added > 0 {
		v.Summary = append(v.Summary, loc.N("servers.plan.sum.added", int64(added)))
	}
	if removed > 0 {
		v.Summary = append(v.Summary, loc.N("servers.plan.sum.removed", int64(removed)))
	}
	if n := len(p.NewGenerated); n > 0 {
		v.Summary = append(v.Summary, loc.N("servers.plan.sum.generated", int64(n)))
	}
	for _, e := range p.Endpoints {
		if e.URIChanged {
			v.Summary = append(v.Summary, i18n.T(ctx, "servers.plan.sum.uri"))
			break
		}
	}
	if p.Force && p.UploadCount > 0 {
		v.Summary = append(v.Summary, loc.N("servers.plan.sum.forced", int64(p.UploadCount)))
	}
	_ = changed

	v.ParamsAdded, v.ParamsRemoved, v.ParamsChanged = p.ParamsAdded, p.ParamsRemoved, p.ParamsChanged
	v.NewGenerated = p.NewGenerated
	for _, e := range p.Endpoints {
		ev := endpointChangeView{Key: e.Key}
		switch {
		case e.Change == "removed":
			ev.Text, ev.Warn = i18n.T(ctx, "servers.plan.ep.removed", i18n.Args{"key": e.Key}), true
		case e.Change == "added":
			ev.Text = i18n.T(ctx, "servers.plan.ep.added", i18n.Args{"key": e.Key})
		case e.URIChanged:
			ev.Text, ev.Warn = i18n.T(ctx, "servers.plan.ep.uri", i18n.Args{"key": e.Key}), true
		default:
			ev.Text = i18n.T(ctx, "servers.plan.ep.changed", i18n.Args{"key": e.Key})
		}
		v.Endpoints = append(v.Endpoints, ev)
	}
	for _, w := range p.Warnings {
		v.Warnings = append(v.Warnings, i18n.T(ctx, w.Key, w.Args))
	}
	for _, st := range p.Steps {
		text := i18n.T(ctx, "job.servers.deploy.step."+st)
		if st == "upload-files" {
			text = loc.N("servers.plan.step.upload", int64(p.UploadCount))
		}
		v.Steps = append(v.Steps, text)
	}
	v.Missing = len(p.MissingParams) > 0 || len(p.Problems) > 0

	fields := map[string]string{
		"kind": v.Kind, "version": strconv.Itoa(p.ToVersion), "hash": p.Hash,
	}
	for k, val := range t.Params {
		fields["param."+k] = val
	}
	href := serverHref(v.S.ID) + "/apply"
	switch {
	case v.Missing:
		// The form above asks first.
	case p.Empty:
		forced := map[string]string{}
		for k, val := range fields {
			forced[k] = val
		}
		forced["force"] = "1"
		forced["hash"] = ""
		v.Force = &ui.Action{
			ID: "server.force", Label: "servers.plan.force", Method: "POST", Href: href, Fields: forced,
			Confirm: &ui.Confirm{Strength: 1, Title: i18n.T(ctx, "servers.plan.force"), Body: i18n.T(ctx, "servers.plan.force.body", i18n.Args{"name": v.S.Name})},
		}
	default:
		if p.Force {
			fields["force"] = "1"
		}
		v.Apply = &ui.Action{ID: "server.apply", Label: "servers.plan.apply." + v.Kind, Method: "POST", Href: href, Fields: fields, Kind: ui.Primary, Key: "a"}
	}
}

// noStore keeps a page that may carry secret values out of caches.
func (h *handler) noStore(c *echo.Context) {
	c.Response().Header().Set("Cache-Control", "no-store")
}

// -------------------------------------------------------------- operations

func (h *handler) op(c *echo.Context, run func(ctx context.Context, id int64) (int64, error)) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	job, err := run(c.Request().Context(), s.ID)
	switch {
	case errors.Is(err, deploy.ErrNotActive), errors.Is(err, deploy.ErrNotAllowed):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case err != nil:
		return notFound(err)
	}
	return web.Redirect(c, serverHref(s.ID)+"?job="+i64(job))
}

func (h *handler) opRestart(c *echo.Context) error {
	return h.op(c, func(ctx context.Context, id int64) (int64, error) { return h.Deploy.Restart(ctx, id, "admin") })
}

func (h *handler) opImages(c *echo.Context) error {
	return h.op(c, func(ctx context.Context, id int64) (int64, error) { return h.Deploy.UpdateImages(ctx, id, "admin") })
}

func (h *handler) opReboot(c *echo.Context) error {
	return h.op(c, func(ctx context.Context, id int64) (int64, error) { return h.Deploy.Reboot(ctx, id, "admin") })
}

// opLogs opens the job page: the logs are the job's log.
func (h *handler) opLogs(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	job, err := h.Deploy.ContainerLogs(c.Request().Context(), s.ID, "admin")
	switch {
	case errors.Is(err, deploy.ErrNotAllowed):
		return echo.NewHTTPError(http.StatusConflict, "the server has no stack")
	case err != nil:
		return notFound(err)
	}
	return web.Redirect(c, "/jobs/"+i64(job))
}

// ---------------------------------------------------------------- rotation

type rotateView struct {
	S        store.Server
	Flag     string
	Keys     []string
	Usage    string
	Warning  string
	Rotate   ui.Action
	Disabled bool
}

func (h *handler) rotatePage(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	keys, err := h.Deploy.RotatableKeys(ctx, s.ID)
	if err != nil {
		return err
	}
	v := rotateView{S: s, Flag: flagOf(s), Keys: keys, Disabled: len(keys) == 0}
	v.Usage = i18n.T(ctx, "servers.rotate.usage_none")
	if h.Usage != nil {
		if u := h.Usage(); u != nil {
			subs, links, err := u.Usage(ctx, s.ID)
			if err != nil {
				return err
			}
			if links > 0 || len(subs) > 0 {
				v.Usage = i18n.T(ctx, "servers.rotate.usage", i18n.Args{"links": links, "subs": len(subs)})
			}
		}
	}
	v.Warning = i18n.T(ctx, "servers.rotate.warning")
	v.Rotate = ui.Action{ID: "server.rotate.start", Label: "servers.rotate.start", Method: "POST", Href: serverHref(s.ID) + "/rotate", Kind: ui.Primary}
	return web.Render(c, http.StatusOK, rotatePageView(h.shell(c, i18n.T(ctx, "servers.rotate.title", i18n.Args{"name": s.Name}), "/servers"), v))
}

func (h *handler) rotatePost(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	job, err := h.Deploy.Rotate(c.Request().Context(), s.ID, "admin")
	switch {
	case errors.Is(err, deploy.ErrNothingToRotate):
		return echo.NewHTTPError(http.StatusConflict, "the template has no rotatable value")
	case errors.Is(err, deploy.ErrNotActive):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case err != nil:
		return notFound(err)
	}
	return web.Redirect(c, serverHref(s.ID)+"?job="+i64(job))
}

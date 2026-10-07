package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// UsageReader names the subscriptions and links that serve a server. Phase 2
// implements it; the pages show "—" while nothing does.
type UsageReader interface {
	Usage(ctx context.Context, serverID int64) (subscriptions []string, links int, err error)
}

func (h *handler) registerServers(r web.Routes) {
	r.Admin.GET("/servers", h.serversList)
	r.Admin.GET("/servers/new", h.newServer)
	r.Admin.POST("/servers/new/summary", h.newSummary)
	r.Admin.POST("/servers/new/location", h.newLocation)
	r.Admin.POST("/servers", h.createServer)
	r.Admin.GET("/servers/:id", h.serverPage)
	r.Admin.GET("/servers/:id/retry", h.retryForm)
	r.Admin.POST("/servers/:id/retry", h.retry)
	r.Admin.POST("/servers/:id/activate", h.activate)
	r.Admin.POST("/servers/:id/cancel", h.cancelProvisioning)
	r.Admin.GET("/servers/:id/endpoints/:key/uri", h.endpointURI)
	r.Admin.GET("/servers/:id/endpoints/:key/qr", h.endpointQR)
	r.Admin.POST("/servers/:id/notes", h.saveNotes)
}

func serverHref(id int64) string { return "/servers/" + i64(id) }

// stateKind maps a server to the marker kind and the word of its status: the
// health for an active server, the lifecycle state otherwise.
func stateKind(ctx context.Context, s store.Server) (kind, word string) {
	if s.State == "active" {
		h := s.Health
		if h == "" {
			h = "unknown"
		}
		kind = map[string]string{"healthy": "ok", "degraded": "look", "blocked": "blocked", "down": "broken", "unknown": "unknown", "paused": "paused"}[h]
		return kind, i18n.T(ctx, "servers.health."+h)
	}
	kind = map[string]string{"provisioning": "running", "failed": "broken", "retired": "gone"}[s.State]
	return kind, i18n.T(ctx, "servers.state."+s.State)
}

// ---------------------------------------------------------------- the list

type serverRow struct {
	Href, Flag, Name string
	Kind, Word       string
	Since            string
	IP, Proxy        string
	Template         string
	Hint             string
}

type serversView struct {
	Rows        []serverRow
	Chips       []ui.Chip
	ShowRetired bool
}

func (h *handler) serversList(c *echo.Context) error {
	ctx := c.Request().Context()
	all, err := store.ListServers(ctx, h.Store.DB.R)
	if err != nil {
		return err
	}
	v := serversView{ShowRetired: c.QueryParam("retired") == "1"}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	loc := i18n.From(ctx)
	for _, s := range all {
		if s.State == "retired" && !v.ShowRetired {
			continue
		}
		kind, word := stateKind(ctx, s)
		row := serverRow{
			Href: serverHref(s.ID), Flag: country.Flag(s.Country), Name: s.Name, Kind: kind, Word: word, IP: s.IP, Proxy: s.ProxyHostname,
			Template: s.TemplateName + " " + vlabel(s.TemplateVersion), Hint: s.Name,
		}
		if s.State == "active" && !s.HealthSince.IsZero() {
			row.Since = i18n.T(ctx, "servers.since", i18n.Args{"ago": loc.Ago(s.HealthSince.Time)})
		}
		v.Rows = append(v.Rows, row)
	}
	retired := ui.Chip{Label: i18n.T(ctx, "servers.chip.retired"), Href: "/servers?retired=1", On: v.ShowRetired}
	if v.ShowRetired {
		retired.Href = "/servers"
	}
	v.Chips = []ui.Chip{retired}
	return web.Render(c, http.StatusOK, serversPage(h.shell(c, i18n.T(ctx, "servers.title"), "/servers"), v))
}

// ------------------------------------------------------------ the server page

type endpointView struct {
	Key, Display, Type string
	Address            string
	URIHref, QRHref    string
}

type jobSide struct {
	ID     int64
	State  string
	Steps  []ui.JobStep
	Lines  []ui.LogLine
	Stream string // set while the job runs
	Reload bool
	Href   string
}

type eventLine struct{ Time, Text string }

// rollbackBand is the notice that the latest change failed and can be rolled back.
type rollbackBand struct {
	Kind, Error, JobHref string
}

type serverView struct {
	S                 store.Server
	Flag              string
	Kind, Word        string
	Reason            string
	Job               *jobSide
	Endpoints         []endpointView
	Events            []eventLine
	Usage             string
	Created, Active   string
	CanRetry          bool
	CanActivateAnyway bool
	CanCancel         bool
	Saved             bool
	Err               string
	// 1e: the change in progress (or the one the page was opened for), the
	// actions, the roll back notice and the default version when newer.
	Change   *jobSide
	Actions  []ui.Action
	Rollback *rollbackBand
	UpdateTo int
}

func (h *handler) serverID(c *echo.Context) (int64, error) { return h.id(c) }

func (h *handler) loadServer(c *echo.Context) (store.Server, error) {
	id, err := h.serverID(c)
	if err != nil {
		return store.Server{}, err
	}
	s, err := store.GetServer(c.Request().Context(), h.Store.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Server{}, echo.ErrNotFound
	}
	return s, err
}

func (h *handler) serverPage(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	job, _ := strconv.ParseInt(c.QueryParam("job"), 10, 64)
	v, err := h.buildServer(c.Request().Context(), s, job)
	if err != nil {
		return err
	}
	v.Saved = c.QueryParam("saved") == "notes"
	return h.renderServer(c, http.StatusOK, v)
}

func (h *handler) renderServer(c *echo.Context, status int, v serverView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, serverOverviewPage(h.shell(c, v.S.Name+" · "+i18n.T(ctx, "servers.title"), "/servers"), v))
}

// headView is what the title block of every tab of a server needs.
func (h *handler) headView(ctx context.Context, s store.Server) serverView {
	loc := i18n.From(ctx)
	v := serverView{S: s, Flag: country.Flag(s.Country), Usage: "—", Created: loc.Time(s.CreatedAt.Time)}
	v.Kind, v.Word = stateKind(ctx, s)
	if !s.ActivatedAt.IsZero() {
		v.Active = loc.Time(s.ActivatedAt.Time)
	}
	v.Reason = s.HealthReason
	v.CanRetry = s.State == "failed"
	v.CanActivateAnyway = s.State == "failed" && s.FailedStep == provision.StepSmokeTest
	v.CanCancel = s.State == "provisioning" && s.ProvisionJobID.Valid
	return v
}

func (h *handler) buildServer(ctx context.Context, s store.Server, jobParam int64) (serverView, error) {
	loc := i18n.From(ctx)
	v := h.headView(ctx, s)

	if h.Usage != nil {
		if u := h.Usage(); u != nil {
			subs, links, err := u.Usage(ctx, s.ID)
			if err == nil {
				v.Usage = i18n.T(ctx, "servers.usage", i18n.Args{"subs": len(subs), "links": links})
			}
		}
	}

	if (s.State == "provisioning" || s.State == "failed") && s.ProvisionJobID.Valid && h.Jobs != nil {
		job, err := h.jobSide(ctx, s.ProvisionJobID.Int64)
		if err != nil {
			return v, err
		}
		v.Job = job
	}

	if err := h.changeView(ctx, s, jobParam, &v); err != nil {
		return v, err
	}

	rows, err := store.Endpoints(ctx, h.Store.DB.R, s.ID)
	if err != nil {
		return v, err
	}
	for _, r := range rows {
		v.Endpoints = append(v.Endpoints, endpointView{
			Key: r.Key, Display: r.DisplayName, Type: r.Type, Address: fmt.Sprintf("%s:%d", r.Host, r.Port),
			URIHref: serverHref(s.ID) + "/endpoints/" + r.Key + "/uri", QRHref: serverHref(s.ID) + "/endpoints/" + r.Key + "/qr",
		})
	}

	list, err := events.List(ctx, h.Store.DB.R, events.Filter{Subject: store.ServerSubject(s.ID), Limit: 8})
	if err != nil {
		return v, err
	}
	for _, e := range list {
		args := i18n.Args{"subject": s.Name, "actor": e.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		text := e.Type
		if key := "event." + e.Type; loc.Has(key) {
			text = loc.T(key, args)
		}
		v.Events = append(v.Events, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return v, nil
}

// jobSide is the steps and the log of a server's provisioning job, in the
// shape the job page's live stream updates.
func (h *handler) jobSide(ctx context.Context, id int64) (*jobSide, error) {
	j, err := h.Jobs.Job(ctx, id)
	if err != nil {
		return nil, err
	}
	loc := i18n.From(ctx)
	steps, err := h.Jobs.Steps(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &jobSide{ID: id, State: string(j.State), Href: "/jobs/" + i64(id)}
	for _, st := range steps {
		sv := ui.JobStep{Name: i18n.T(ctx, "job."+j.Type+".step."+st.Name), State: string(st.State), Kind: stepKind(string(st.State))}
		if !st.FinishedAt.IsZero() && !st.StartedAt.IsZero() {
			sv.Time = loc.Duration(st.FinishedAt.Sub(st.StartedAt.Time))
		}
		out.Steps = append(out.Steps, sv)
	}
	lines, err := h.Jobs.LogTail(ctx, id, 200)
	if err != nil {
		return nil, err
	}
	var last int64
	for _, l := range lines {
		out.Lines = append(out.Lines, ui.LogLine{ID: l.ID, Time: loc.Clock(l.Time.Time), Level: l.Level, Text: l.Text})
		last = l.ID
	}
	if j.State == jobs.Queued || j.State == jobs.Running || j.State == jobs.Interrupted {
		out.Stream = "/jobs/" + i64(id) + "/stream?after=" + i64(last)
		out.Reload = true
	}
	return out, nil
}

func stepKind(state string) string {
	switch state {
	case "succeeded":
		return "ok"
	case "running":
		return "running"
	case "failed":
		return "broken"
	case "cancelled":
		return "off"
	}
	return "unknown"
}

// -------------------------------------------------------------- endpoints

func (h *handler) loadEndpoint(c *echo.Context) (store.Server, endpoint.Endpoint, error) {
	s, err := h.loadServer(c)
	if err != nil {
		return s, endpoint.Endpoint{}, err
	}
	rows, err := store.Endpoints(c.Request().Context(), h.Store.DB.R, s.ID)
	if err != nil {
		return s, endpoint.Endpoint{}, err
	}
	for _, r := range rows {
		if r.Key == c.Param("key") {
			e, err := sealed.OpenEndpoint(h.Vault, r)
			return s, e, err
		}
	}
	return s, endpoint.Endpoint{}, echo.ErrNotFound
}

func (h *handler) uriOf(e endpoint.Endpoint) (string, error) {
	t, ok := endpoint.Lookup(e.Type)
	if !ok {
		return "", fmt.Errorf("unknown endpoint type %q", e.Type)
	}
	return t.URI(e), nil
}

type uriView struct {
	Key, URI string
	QRHref   string
}

// endpointURI answers the Reveal button: the URI in a fragment that is never
// cached. It records nothing: showing a secret on a signed-in page is not a change.
func (h *handler) endpointURI(c *echo.Context) error {
	s, e, err := h.loadEndpoint(c)
	if err != nil {
		return err
	}
	uri, err := h.uriOf(e)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, uriRevealed(uriView{Key: e.Key, URI: uri, QRHref: serverHref(s.ID) + "/endpoints/" + e.Key + "/qr"}))
}

func (h *handler) endpointQR(c *echo.Context) error {
	_, e, err := h.loadEndpoint(c)
	if err != nil {
		return err
	}
	uri, err := h.uriOf(e)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, qrDialog(e.DisplayName, uri))
}

// ----------------------------------------------------------------- actions

func (h *handler) saveNotes(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	err = h.Store.SetServerNotes(ctx, s.ID, strings.ReplaceAll(c.FormValue("notes"), "\r\n", "\n"), "admin")
	if errs, ok := fieldErrors(c, err); ok {
		v, berr := h.buildServer(ctx, s, 0)
		if berr != nil {
			return berr
		}
		v.S.Notes = c.FormValue("notes")
		v.Err = errs["notes"]
		return h.renderServer(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, serverHref(s.ID)+"?saved=notes")
}

func (h *handler) activate(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	err = h.Provision.ActivateAnyway(c.Request().Context(), s.ID, "admin")
	if errors.Is(err, provision.ErrNotAllowed) {
		return echo.NewHTTPError(http.StatusConflict, "activate anyway is for a server that failed only at the smoke test")
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, serverHref(s.ID))
}

func (h *handler) cancelProvisioning(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	if s.State != "provisioning" || !s.ProvisionJobID.Valid {
		return echo.NewHTTPError(http.StatusConflict, "nothing is being provisioned")
	}
	if err := h.Jobs.Cancel(c.Request().Context(), s.ProvisionJobID.Int64, "admin"); err != nil && !errors.Is(err, jobs.ErrNotActive) {
		return err
	}
	return web.Redirect(c, serverHref(s.ID))
}

// ------------------------------------------------------------------- retry

type retryView struct {
	S             store.Server
	NeedsPassword bool
	Conflict      bool
	Errs          map[string]string
}

func (h *handler) retryForm(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	if s.State != "failed" {
		return web.Redirect(c, serverHref(s.ID))
	}
	return h.renderRetry(c, http.StatusOK, s, nil)
}

func (h *handler) renderRetry(c *echo.Context, status int, s store.Server, errs map[string]string) error {
	ctx := c.Request().Context()
	v := retryView{S: s, NeedsPassword: provision.NeedsPassword(s.FailedStep), Errs: errs,
		Conflict: s.FailedStep == provision.StepDNS && strings.Contains(s.FailedError, "already points to")}
	return web.Render(c, status, retryPage(h.shell(c, i18n.T(ctx, "servers.retry.title", i18n.Args{"name": s.Name}), "/servers"), v))
}

func (h *handler) retry(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	_, err = h.Provision.Retry(c.Request().Context(), s.ID, c.FormValue("root_password"), c.FormValue("overwrite_dns") == "1", "admin")
	var fe provision.FieldErrors
	switch {
	case errors.As(err, &fe):
		return h.renderRetry(c, http.StatusUnprocessableEntity, s, translate(c, fe))
	case errors.Is(err, provision.ErrNotAllowed):
		return web.Redirect(c, serverHref(s.ID))
	case err != nil:
		return err
	}
	return web.Redirect(c, serverHref(s.ID))
}

// translate turns provision.FieldErrors into the texts a page shows.
func translate(c *echo.Context, fe provision.FieldErrors) map[string]string {
	ctx := c.Request().Context()
	out := make(map[string]string, len(fe))
	for k, m := range fe {
		out[k] = i18n.T(ctx, m.Key, m.Args)
	}
	return out
}

// DashboardServers is the body of the servers area on the dashboard.
func DashboardServers(counts map[string]int, failed []store.Server) templ.Component {
	return dashboardServers(counts, failed)
}

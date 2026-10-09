package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const discoverPath = "/routing/discover"

func runHref(id int64) string { return discoverPath + "/" + i64(id) }

func (h *handler) registerDiscover(r web.Routes) {
	r.Admin.GET(discoverPath, h.discoverForm)
	r.Admin.POST(discoverPath, h.discoverStart)
	r.Admin.GET(discoverPath+"/:id", h.runPage)
	r.Admin.GET(discoverPath+"/:id/status", h.runStatus)
	r.Admin.GET(discoverPath+"/:id/screenshots/:file", h.runScreenshot)
	r.Admin.POST(discoverPath+"/:id/create", h.runCreate)
	r.Admin.POST(discoverPath+"/:id/add", h.runAdd)
	r.Admin.POST(discoverPath+"/:id/again", h.runAgain)
}

// viaOption is a way to visit on the form and in Visit again through….
type viaOption struct {
	Value, Label, Note string // value: direct, auto, server:<id>
	Disabled           bool
}

type recentRun struct {
	ID               int64
	Host, Line       string
	StatusKind, Word string
}

type discoverForm struct {
	Website, Via string
	Depth        int
	Options      []viaOption
	Chromium     string
	ChromiumOK   bool
	Errs         map[string]string
	Recent       []recentRun
}

// chromiumLine says whether visits run, as the Integrations row does.
func (h *handler) chromiumLine(ctx context.Context) (string, bool) {
	state, detail := h.Discovery.Chromium(ctx)
	return i18n.T(ctx, "discovery.chromium."+state, i18n.Args{"detail": detail}), state == discovery.ChromiumConnected
}

// viaOptions are Direct, Auto (with servers) and every active server by
// name, the unhealthy ones disabled with their health.
func (h *handler) viaOptions(ctx context.Context) ([]viaOption, error) {
	out := []viaOption{{Value: discovery.ViaDirect, Label: i18n.T(ctx, "discovery.via.direct"), Note: i18n.T(ctx, "discovery.via.direct.note")}}
	if !h.Discovery.ThroughServers() {
		return out, nil
	}
	out = append(out, viaOption{Value: discovery.ViaAuto, Label: i18n.T(ctx, "discovery.via.auto"), Note: i18n.T(ctx, "discovery.via.auto.note")})
	list, err := h.Discovery.Servers(ctx)
	if err != nil {
		return nil, err
	}
	for _, se := range list {
		o := viaOption{Value: "server:" + i64(se.ServerID), Label: i18n.T(ctx, "discovery.via.server", i18n.Args{"name": strings.TrimSpace(se.Flag + " " + se.Name)})}
		if se.Health != "healthy" {
			o.Disabled, o.Note = true, i18n.T(ctx, "discovery.via.unhealthy", i18n.Args{"health": se.Health})
		}
		out = append(out, o)
	}
	return out, nil
}

// parseVia reads "direct", "auto" or "server:<id>".
func parseVia(v string) (string, int64) {
	if id, ok := strings.CutPrefix(v, "server:"); ok {
		n, _ := strconv.ParseInt(id, 10, 64)
		return discovery.ViaServer, n
	}
	return v, 0
}

func (h *handler) discoverForm(c *echo.Context) error {
	return h.renderDiscoverForm(c, http.StatusOK, discoverForm{Website: c.QueryParam("website"), Via: discovery.ViaDirect})
}

func (h *handler) renderDiscoverForm(c *echo.Context, status int, v discoverForm) error {
	ctx := c.Request().Context()
	var err error
	if v.Options, err = h.viaOptions(ctx); err != nil {
		return err
	}
	v.Chromium, v.ChromiumOK = h.chromiumLine(ctx)
	runs, err := h.Discovery.Recent(ctx, 20)
	if err != nil {
		return err
	}
	for _, r := range runs {
		k, w := runState(ctx, r)
		v.Recent = append(v.Recent, recentRun{ID: r.ID, Host: r.Host, StatusKind: k, Word: w,
			Line: i18n.T(ctx, "discovery.recent.line", i18n.Args{"id": r.ID, "via": viaWord(ctx, r), "when": i18n.From(ctx).Ago(r.CreatedAt)})})
	}
	return web.Render(c, status, discoverPage(h.shell(c, i18n.T(ctx, "discovery.title"), discoverPath), v))
}

func (h *handler) discoverStart(c *echo.Context) error {
	ctx := c.Request().Context()
	via, sid := parseVia(c.FormValue("via"))
	depth, _ := strconv.Atoi(c.FormValue("depth"))
	id, err := h.Discovery.Start(ctx, discovery.Start{Website: c.FormValue("website"), Via: via, ServerID: sid, Depth: depth}, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		return h.renderDiscoverForm(c, http.StatusUnprocessableEntity, discoverForm{
			Website: c.FormValue("website"), Via: c.FormValue("via"), Depth: depth, Errs: errText(ctx, fe),
		})
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, runHref(id))
}

// runState is the run's marker and word.
func runState(ctx context.Context, r discovery.Run) (string, string) {
	switch r.State {
	case discovery.Queued:
		return "unknown", i18n.T(ctx, "discovery.state.queued")
	case discovery.Running:
		return "running", i18n.T(ctx, "discovery.state.running")
	case discovery.Failed:
		return "broken", i18n.T(ctx, "discovery.state.failed")
	}
	return "ok", i18n.T(ctx, "discovery.state.done")
}

func viaWord(ctx context.Context, r discovery.Run) string {
	if r.Via == discovery.ViaServer {
		return r.ServerName
	}
	return i18n.T(ctx, "discovery.via."+r.Via)
}

func (h *handler) loadRun(c *echo.Context) (discovery.Run, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return discovery.Run{}, echo.ErrNotFound
	}
	r, err := h.Discovery.Get(c.Request().Context(), id)
	if errors.Is(err, discovery.ErrNotFound) {
		return r, echo.ErrNotFound
	}
	return r, err
}

type suggestionRow struct {
	Selector, Line, Lists, Href string
}

type visitRow struct {
	Path, Line, Note string
	OK               bool
	Shots            []string
}

type hostRow struct {
	Host, Class, Requests, Failed, Covered string
	Tickable, Ticked                       bool
}

type groupRow struct {
	Domain, Hosts, Class, Requests, Failed, Covered string
	Ticked                                          bool
	Rows                                            []hostRow
}

type customChoice struct {
	ID   int64
	Name string
}

type runView struct {
	Run                discovery.Run
	Title, State, Kind string
	Line               string
	Active             bool
	NoBrowser          string
	Again              []viaOption
	Suggestions        []suggestionRow
	SuggestNote        string
	Visits             []visitRow
	Groups             []groupRow
	IPs, Capped        string
	Name, Tag          string
	Lists              []listChoice
	Customs            []customChoice
	Err                string
	Errs               map[string]string
	SaveNote           string
}

// listChoice is a routing list with its targets: "Main · Home, iPhone".
type listChoice struct {
	ID      int64
	Name    string
	Targets string
	Checked bool
}

func (h *handler) runView(ctx context.Context, r discovery.Run) (runView, error) {
	loc := i18n.From(ctx)
	v := runView{Run: r, Active: r.Active(), Errs: map[string]string{}}
	v.Kind, v.State = runState(ctx, r)
	switch r.State {
	case discovery.Done, discovery.Failed:
		took := r.FinishedAt.Sub(r.CreatedAt).Round(time.Second)
		v.Title = i18n.T(ctx, "discovery.finished", i18n.Args{"at": loc.Time(r.FinishedAt), "took": loc.Duration(took)})
	default:
		v.Title = i18n.T(ctx, "discovery.started", i18n.Args{"at": loc.Time(r.CreatedAt)})
	}
	depth := i18n.T(ctx, "discovery.depth.home")
	if r.Depth > 0 {
		depth = i18n.T(ctx, "discovery.depth.links")
	}
	v.Line = i18n.T(ctx, "discovery.run.line", i18n.Args{"url": r.URL, "via": viaWord(ctx, r), "depth": depth, "registrable": r.Registrable})
	if !h.Discovery.CanVisit() {
		v.NoBrowser = i18n.T(ctx, "discovery.no_browser")
	}
	var err error
	if v.Again, err = h.viaOptions(ctx); err != nil {
		return v, err
	}
	for _, s := range r.Suggestions {
		v.Suggestions = append(v.Suggestions, suggestionView(ctx, s))
	}
	if len(r.Suggestions) > 0 {
		v.SuggestNote = i18n.T(ctx, "discovery.suggest.note")
	}
	for _, vr := range r.Visits {
		row := visitRow{Path: vr.Path, OK: vr.Loaded}
		if vr.Path == discovery.ViaDirect {
			row.Path = i18n.T(ctx, "discovery.visit.direct")
		} else {
			row.Path = i18n.T(ctx, "discovery.visit.through", i18n.Args{"name": vr.Path})
		}
		if vr.Loaded {
			row.Line = i18n.T(ctx, "discovery.visit.loaded", i18n.Args{
				"pages": loc.N("discovery.n.pages", int64(vr.Pages)), "requests": loc.N("discovery.n.requests", int64(vr.Requests)),
				"hosts": loc.N("discovery.n.hosts", int64(vr.Hosts)),
			})
		} else {
			row.Line = i18n.T(ctx, "discovery.visit.never", i18n.Args{"error": vr.Error})
		}
		if vr.Failed > 0 {
			row.Note = i18n.T(ctx, "discovery.visit.failed", i18n.Args{"failed": vr.Failed, "hosts": vr.Hosts})
		}
		for _, s := range vr.Screenshots {
			row.Shots = append(row.Shots, runHref(r.ID)+"/screenshots/"+s)
		}
		v.Visits = append(v.Visits, row)
	}
	if len(r.Visits) == 2 && !r.Visits[0].Loaded || len(r.Visits) == 2 && r.Visits[0].Failed > 0 {
		v.Visits[0].Note = strings.TrimSpace(v.Visits[0].Note + " " + i18n.T(ctx, "discovery.visit.repeated", i18n.Args{"name": r.Visits[1].Path}))
	}
	for _, g := range r.Groups {
		gr := groupRow{Domain: g.Registrable, Class: i18n.T(ctx, "discovery.class."+string(g.Class)), Ticked: g.Ticked,
			Hosts: loc.N("discovery.n.hosts", int64(len(g.Hosts))), Requests: loc.Number(int64(g.Requests)), Covered: strings.Join(g.CoveredBy, ", ")}
		if g.Failed > 0 {
			gr.Failed = i18n.T(ctx, "discovery.group.failed", i18n.Args{"n": g.Failed, "of": len(g.Hosts)})
		}
		for _, hst := range g.Hosts {
			hr := hostRow{Host: hst.Host, Class: i18n.T(ctx, "discovery.class."+string(hst.Class)), Requests: loc.Number(int64(hst.Requests)),
				Tickable: hst.Tickable(), Ticked: !g.Ticked && hst.Ticked(), Covered: strings.Join(hst.CoveredBy, ", ")}
			if hst.Failed != "" {
				hr.Failed = i18n.T(ctx, "discovery.host.failed", i18n.Args{"reason": hst.Failed, "n": hst.FailedRequests})
			}
			gr.Rows = append(gr.Rows, hr)
		}
		v.Groups = append(v.Groups, gr)
	}
	if len(r.IPs) > 0 {
		var parts []string
		for _, ip := range r.IPs {
			parts = append(parts, ip.Host+" · "+loc.N("discovery.n.requests", int64(ip.Requests)))
		}
		v.IPs = i18n.T(ctx, "discovery.ips", i18n.Args{"ips": strings.Join(parts, ", ")})
	}
	if r.Capped {
		v.Capped = i18n.T(ctx, "discovery.capped", i18n.Args{"n": discovery.MaxHosts})
	}
	v.Name = discovery.DefaultName(r)
	v.Tag = selector.Slug(v.Name)
	if v.Lists, err = h.listChoices(ctx, nil, true); err != nil {
		return v, err
	}
	var names []string
	for _, l := range v.Lists {
		if l.Checked && l.Targets != "" {
			names = append(names, l.Targets)
		}
	}
	v.SaveNote = i18n.T(ctx, "discovery.save.note")
	if len(names) > 0 {
		v.SaveNote = i18n.T(ctx, "discovery.save.note_targets", i18n.Args{"targets": strings.Join(names, ", ")})
	}
	customs, err := h.Services.List(ctx, services.Filter{Source: string(selector.Custom)})
	if err != nil {
		return v, err
	}
	for _, cs := range customs {
		v.Customs = append(v.Customs, customChoice{ID: cs.ID, Name: cs.Tag})
	}
	return v, nil
}

// listChoices are the routing lists with their targets' names.
func (h *handler) listChoices(ctx context.Context, checked []int64, first bool) ([]listChoice, error) {
	checks, err := h.listChecks(ctx, checked, first)
	if err != nil {
		return nil, err
	}
	out := make([]listChoice, 0, len(checks))
	for _, l := range checks {
		ts, err := h.Lists.Targets(ctx, l.ID)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, t := range ts {
			names = append(names, t.Name)
		}
		out = append(out, listChoice{ID: l.ID, Name: l.Name, Targets: strings.Join(names, ", "), Checked: l.Checked})
	}
	return out, nil
}

func suggestionView(ctx context.Context, s catalog.Suggestion) suggestionRow {
	loc := i18n.From(ctx)
	row := suggestionRow{Selector: s.Selector, Href: listPath + "/add?selector=" + url.QueryEscape(s.Selector)}
	parts := []string{i18n.T(ctx, "discovery.match."+s.Match, i18n.Args{"name": s.Name}), loc.N("catalog.line.list", int64(s.Domains))}
	if s.Portal != "" {
		parts = append(parts, s.Portal)
	}
	if len(s.Via) > 0 {
		parts = append(parts, i18n.T(ctx, "discovery.also_in", i18n.Args{"lists": strings.Join(s.Via, ", ")}))
	}
	row.Line = strings.Join(parts, " · ")
	switch {
	case s.Service == nil:
		row.Lists = i18n.T(ctx, "catalog.not_a_service")
	case len(s.Lists) == 0:
		row.Lists = i18n.T(ctx, "discovery.in_no_list", i18n.Args{"tag": s.Service.Tag})
		row.Href = svcHref(s.Service.ID)
	default:
		row.Lists = i18n.T(ctx, "discovery.in_lists", i18n.Args{"tag": s.Service.Tag, "lists": strings.Join(s.Lists, ", ")})
		row.Href = svcHref(s.Service.ID)
	}
	return row
}

func (h *handler) runPage(c *echo.Context) error {
	r, err := h.loadRun(c)
	if err != nil {
		return err
	}
	return h.renderRun(c, http.StatusOK, r, nil)
}

func (h *handler) renderRun(c *echo.Context, status int, r discovery.Run, edit func(*runView)) error {
	ctx := c.Request().Context()
	v, err := h.runView(ctx, r)
	if err != nil {
		return err
	}
	if edit != nil {
		edit(&v)
	}
	s := h.shell(c, i18n.T(ctx, "discovery.run.title", i18n.Args{"host": r.Host}), discoverPath)
	s.PageKeys = "discovery.keys"
	return web.Render(c, status, runPage(s, v))
}

// runStatus is the polled part of the run page.
func (h *handler) runStatus(c *echo.Context) error {
	r, err := h.loadRun(c)
	if err != nil {
		return err
	}
	v, err := h.runView(c.Request().Context(), r)
	if err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, runPoll(v))
}

func (h *handler) runScreenshot(c *echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	p, err := h.Discovery.Screenshot(id, c.Param("file"))
	if err != nil {
		return echo.ErrNotFound
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Cache-Control", "private, max-age=86400")
	return c.Blob(http.StatusOK, "image/jpeg", b)
}

func formPick(c *echo.Context) discovery.Pick {
	f, _ := c.FormValues()
	return discovery.Pick{Suffix: f["group"], Exact: f["host"]}
}

// saveError words a failed save; ok is false for an error that isn't the admin's.
func saveError(ctx context.Context, err error) (string, map[string]string, bool) {
	var ge *lists.GuardError
	var fe store.FieldErrors
	var tt *services.TagTakenError
	switch {
	case errors.As(err, &ge):
		return guardText(ctx, ge), nil, true
	case errors.As(err, &fe):
		return "", errText(ctx, fe), true
	case errors.As(err, &tt):
		return "", map[string]string{"tag": i18n.T(ctx, "services.err.tag_taken")}, true
	case errors.Is(err, discovery.ErrNoPicks):
		return i18n.T(ctx, "discovery.err.no_picks"), nil, true
	case errors.Is(err, discovery.ErrNotDone):
		return i18n.T(ctx, "discovery.err.not_done"), nil, true
	case errors.Is(err, discovery.ErrNotCustom), errors.Is(err, services.ErrNotFound):
		return i18n.T(ctx, "discovery.err.service"), nil, true
	case errors.Is(err, lists.ErrNotFound):
		return i18n.T(ctx, "lists.err.gone"), nil, true
	}
	return "", nil, false
}

func (h *handler) runCreate(c *echo.Context) error {
	r, err := h.loadRun(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	listIDs := formIDs(c, "list")
	p := formPick(c)
	name, tag := c.FormValue("name"), c.FormValue("tag")
	sid, err := h.Discovery.Create(ctx, r.ID, services.Custom{Name: name, Tag: tag}, p, listIDs, events.ActorAdmin)
	if err == nil {
		return web.Redirect(c, svcHref(sid)+"?added=1")
	}
	msg, errs, ok := saveError(ctx, err)
	if !ok {
		return err
	}
	return h.renderRun(c, http.StatusUnprocessableEntity, r, func(v *runView) {
		v.Err, v.Name, v.Tag = msg, name, tag
		for k, e := range errs {
			v.Errs[k] = e
		}
		keepPicks(v, p)
		for i := range v.Lists {
			v.Lists[i].Checked = slices.Contains(listIDs, v.Lists[i].ID)
		}
	})
}

// keepPicks ticks what the admin had ticked.
func keepPicks(v *runView, p discovery.Pick) {
	for i := range v.Groups {
		g := &v.Groups[i]
		g.Ticked = slices.Contains(p.Suffix, g.Domain)
		for j := range g.Rows {
			g.Rows[j].Ticked = slices.Contains(p.Exact, g.Rows[j].Host)
		}
	}
}

func (h *handler) runAdd(c *echo.Context) error {
	r, err := h.loadRun(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	sid, _ := strconv.ParseInt(c.FormValue("service"), 10, 64)
	p := formPick(c)
	err = h.Discovery.AddTo(ctx, r.ID, sid, p, events.ActorAdmin)
	if err == nil {
		return web.Redirect(c, svcHref(sid)+"?saved=1")
	}
	msg, errs, ok := saveError(ctx, err)
	if !ok {
		return err
	}
	return h.renderRun(c, http.StatusUnprocessableEntity, r, func(v *runView) {
		v.Err = msg
		if msg == "" {
			for _, e := range errs {
				v.Err = e
				break
			}
		}
		keepPicks(v, p)
	})
}

func (h *handler) runAgain(c *echo.Context) error {
	r, err := h.loadRun(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	via, sid := parseVia(c.FormValue("via"))
	id, err := h.Discovery.Again(ctx, r.ID, via, sid, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		return h.renderRun(c, http.StatusUnprocessableEntity, r, func(v *runView) {
			for _, e := range errText(ctx, fe) {
				v.Err = e
				break
			}
		})
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, runHref(id))
}

// picked counts the default ticks: the picker's count starts here.
func (v runView) picked() int {
	n := 0
	for _, g := range v.Groups {
		if g.Ticked {
			n++
		}
		for _, h := range g.Rows {
			if h.Ticked {
				n++
			}
		}
	}
	return n
}

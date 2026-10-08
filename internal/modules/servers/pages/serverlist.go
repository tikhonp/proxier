package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ---------------------------------------------------------------- the list

// listFilter is the list's state, all of it in the URL.
type listFilter struct {
	// State: "" = everything but retired, "all", or one lifecycle state
	// (provisioning active failed retiring retired).
	State    string
	Health   string
	Location int64
	Template int64
	Sort     string // name (default) health traffic
}

var (
	listStates  = []string{"active", "provisioning", "failed", "retiring", "retired"}
	listHealths = []string{"healthy", "degraded", "blocked", "down", "unknown", "paused"}
)

func parseListFilter(c *echo.Context) listFilter {
	f := listFilter{State: c.QueryParam("state"), Health: c.QueryParam("health"), Sort: c.QueryParam("sort")}
	if c.QueryParam("retired") == "1" && f.State == "" {
		f.State = "all"
	}
	if f.State != "all" && !has(listStates, f.State) {
		f.State = ""
	}
	if !has(listHealths, f.Health) {
		f.Health = ""
	}
	if f.Sort != "health" && f.Sort != "traffic" {
		f.Sort = ""
	}
	f.Location, _ = strconv.ParseInt(c.QueryParam("location"), 10, 64)
	f.Template, _ = strconv.ParseInt(c.QueryParam("template"), 10, 64)
	return f
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (f listFilter) query() url.Values {
	q := url.Values{}
	if f.State != "" {
		q.Set("state", f.State)
	}
	if f.Health != "" {
		q.Set("health", f.Health)
	}
	if f.Location > 0 {
		q.Set("location", i64(f.Location))
	}
	if f.Template > 0 {
		q.Set("template", i64(f.Template))
	}
	if f.Sort != "" {
		q.Set("sort", f.Sort)
	}
	return q
}

func (f listFilter) href(change func(*listFilter)) string {
	change(&f)
	if q := f.query().Encode(); q != "" {
		return "/servers?" + q
	}
	return "/servers"
}

func (f listFilter) active() bool {
	return f.State != "" || f.Health != "" || f.Location > 0 || f.Template > 0
}

// lifecycle is the state the list filters on: retiring is a state of its own
// here, although the table keeps the server's last state until it is retired.
func lifecycle(s store.Server) string {
	if s.Retiring() {
		return "retiring"
	}
	return s.State
}

func (f listFilter) matches(s store.Server) bool {
	life := lifecycle(s)
	switch {
	case f.State == "" && life == "retired":
		return false
	case f.State != "" && f.State != "all" && f.State != life:
		return false
	case f.Health != "" && (s.State != "active" || s.Health != f.Health):
		return false
	case f.Location > 0 && s.LocationID != f.Location:
		return false
	case f.Template > 0 && s.TemplateID != f.Template:
		return false
	}
	return true
}

type serverRow struct {
	ID                int64
	Href, Flag, Name  string
	Kind, Word        string
	Since, Reason     string
	Lifecycle         string // shown when the server is not active
	IP, Proxy         string
	Template          string
	Update            string // "v3 available"
	ProxyMS           string
	HasSample         bool
	CPU               int64 // per mille
	MemUsed, MemTot   int64
	DiskUsed, DiskTot int64
	Traffic           string
	Subs              string
	Bulk              bool // an active server: the bulk actions can take it
	Hint              string

	traffic int64
	rank    int
}

type serversView struct {
	Rows     []serverRow
	Chips    []ui.Chip
	Form     *ui.FilterForm
	Clear    string
	Total    int
	Notice   string
	Selected map[int64]bool
}

var healthRank = map[string]int{"down": 0, "blocked": 1, "degraded": 2, "unknown": 3, "paused": 4, "healthy": 5}

func (h *handler) serversList(c *echo.Context) error {
	ctx := c.Request().Context()
	f := parseListFilter(c)
	all, err := store.ListServers(ctx, h.Store.DB.R)
	if err != nil {
		return err
	}
	samples := map[int64]store.Sample{}
	if rows, err := store.LatestSamples(ctx, h.Store.DB.R); err == nil {
		for _, sm := range rows {
			samples[sm.ServerID] = sm
		}
	}
	defaults := map[int64]int{} // template → default version, for "update available"
	loc := i18n.From(ctx)

	v := serversView{Total: len(all)}
	for _, s := range all {
		if !f.matches(s) {
			continue
		}
		kind, word := stateKind(ctx, s)
		row := serverRow{
			ID: s.ID, Href: serverHref(s.ID), Flag: country.Flag(s.Country), Name: s.Name, Kind: kind, Word: word, IP: s.IP, Proxy: s.ProxyHostname,
			Template: s.TemplateName + " " + vlabel(s.TemplateVersion), Hint: s.Name, Subs: "—", ProxyMS: "—", Traffic: "—",
			Bulk: s.State == "active" && !s.Retiring(),
		}
		if s.State == "active" && !s.Retiring() {
			row.rank = healthRank[s.Health]
			if !s.HealthSince.IsZero() {
				row.Since = i18n.T(ctx, "servers.since", i18n.Args{"ago": loc.Ago(s.HealthSince.Time)})
			}
			row.Reason = reasonText(ctx, s.HealthReason)
		} else {
			row.rank = 10
			row.Lifecycle = word
		}
		if s.State == "active" && !s.Retiring() {
			def, ok := defaults[s.TemplateID]
			if !ok {
				if info, err := h.Templates.Get(ctx, s.TemplateID); err == nil {
					def = info.DefaultVersion
				}
				defaults[s.TemplateID] = def
			}
			if def > s.TemplateVersion {
				row.Update = i18n.T(ctx, "servers.update_available", i18n.Args{"version": vlabel(def)})
			}
			if ms, ok := h.proxyMS(ctx, s.ID); ok {
				row.ProxyMS = strconv.Itoa(ms) + " ms"
			}
		}
		if sm, ok := samples[s.ID]; ok && s.State == "active" {
			row.HasSample = true
			if sm.CPUPct.Valid {
				row.CPU = int64(sm.CPUPct.Float64 * 10)
			}
			row.MemUsed, row.MemTot, row.DiskUsed, row.DiskTot = sm.MemUsed, sm.MemTotal, sm.DiskUsed, sm.DiskTotal
		}
		if h.Stats != nil && s.State == "active" {
			if rx, tx, err := h.Stats.TrafficToday(ctx, s.ID); err == nil && rx+tx > 0 {
				row.traffic = rx + tx
				row.Traffic = HumanBytes(rx + tx)
			}
		}
		if h.Usage != nil {
			if u := h.Usage(); u != nil {
				if subs, _, err := u.Usage(ctx, s.ID); err == nil {
					row.Subs = strconv.Itoa(len(subs))
				}
			}
		}
		v.Rows = append(v.Rows, row)
	}
	sort.SliceStable(v.Rows, func(i, j int) bool {
		a, b := v.Rows[i], v.Rows[j]
		switch f.Sort {
		case "health":
			if a.rank != b.rank {
				return a.rank < b.rank
			}
		case "traffic":
			if a.traffic != b.traffic {
				return a.traffic > b.traffic
			}
		}
		return a.Name < b.Name
	})

	if err := h.listFilters(ctx, f, &v); err != nil {
		return err
	}
	switch c.QueryParam("bulk") {
	case "checks":
		v.Notice = i18n.T(ctx, "servers.bulk.checks_done", i18n.Args{"n": c.QueryParam("n")})
	case "pause":
		v.Notice = i18n.T(ctx, "servers.bulk.pause_done", i18n.Args{"n": c.QueryParam("n")})
	}
	return web.Render(c, http.StatusOK, serversPage(h.shell(c, i18n.T(ctx, "servers.title"), "/servers"), v))
}

// proxyMS is the first-byte time of the newest passing proxy test.
func (h *handler) proxyMS(ctx context.Context, id int64) (int, bool) {
	rows, err := store.LatestResults(ctx, h.Store.DB.R, id, "proxy")
	if err != nil {
		return 0, false
	}
	best, found := 0, false
	for _, r := range rows {
		if !r.OK || r.Inconclusive {
			continue
		}
		var d struct {
			FirstByteMS int `json:"first_byte_ms"`
		}
		if json.Unmarshal([]byte(r.Detail), &d) == nil && (!found || d.FirstByteMS > best) {
			best, found = d.FirstByteMS, true
		}
	}
	return best, found
}

// listFilters builds the chips and the selects above the list.
func (h *handler) listFilters(ctx context.Context, f listFilter, v *serversView) error {
	showAll := ui.Chip{Label: i18n.T(ctx, "servers.chip.retired"), On: f.State == "all"}
	showAll.Href = f.href(func(g *listFilter) { g.State = "all" })
	if f.State == "all" {
		showAll.Href = f.href(func(g *listFilter) { g.State = "" })
	}
	v.Chips = []ui.Chip{showAll}

	opts := func(first string, vals []string, label func(string) string) []ui.FilterOption {
		out := []ui.FilterOption{{Value: "", Label: first}}
		for _, x := range vals {
			out = append(out, ui.FilterOption{Value: x, Label: label(x)})
		}
		return out
	}
	stateOpts := append(opts(i18n.T(ctx, "servers.filter.state_default"), listStates, func(x string) string { return i18n.T(ctx, "servers.state."+x) }),
		ui.FilterOption{Value: "all", Label: i18n.T(ctx, "servers.filter.state_all")})
	healthOpts := opts(i18n.T(ctx, "servers.filter.health_any"), listHealths, func(x string) string { return i18n.T(ctx, "servers.health."+x) })

	locOpts := []ui.FilterOption{{Value: "", Label: i18n.T(ctx, "servers.filter.location_any")}}
	locs, err := h.Store.Locations(ctx)
	if err != nil {
		return err
	}
	for _, l := range locs {
		locOpts = append(locOpts, ui.FilterOption{Value: i64(l.ID), Label: country.Flag(l.Country) + " " + l.Code + " · " + l.Name})
	}
	tplOpts := []ui.FilterOption{{Value: "", Label: i18n.T(ctx, "servers.filter.template_any")}}
	tpls, err := h.Templates.List(ctx, true)
	if err != nil {
		return err
	}
	for _, t := range tpls {
		tplOpts = append(tplOpts, ui.FilterOption{Value: i64(t.ID), Label: t.Name})
	}
	sortOpts := []ui.FilterOption{
		{Value: "", Label: i18n.T(ctx, "servers.sort.name")},
		{Value: "health", Label: i18n.T(ctx, "servers.sort.health")},
		{Value: "traffic", Label: i18n.T(ctx, "servers.sort.traffic")},
	}
	v.Form = &ui.FilterForm{
		Action: "/servers", Apply: i18n.T(ctx, "jobs.filter.apply"), Hidden: map[string]string{},
		Selects: []ui.FilterSelect{
			{Name: "state", Label: i18n.T(ctx, "servers.filter.state"), Value: f.State, Options: stateOpts},
			{Name: "health", Label: i18n.T(ctx, "servers.filter.health"), Value: f.Health, Options: healthOpts},
			{Name: "location", Label: i18n.T(ctx, "servers.filter.location"), Value: zeroEmpty(f.Location), Options: locOpts},
			{Name: "template", Label: i18n.T(ctx, "servers.filter.template"), Value: zeroEmpty(f.Template), Options: tplOpts},
			{Name: "sort", Label: i18n.T(ctx, "servers.filter.sort"), Value: f.Sort, Options: sortOpts},
		},
	}
	if f.active() || f.Sort != "" {
		v.Clear = "/servers"
	}
	return nil
}

func zeroEmpty(n int64) string {
	if n <= 0 {
		return ""
	}
	return i64(n)
}

// ------------------------------------------------------------ bulk actions

// selected reads the ids a list form posted and loads the servers. A selection
// the bulk actions cannot take (any server that is not active) is refused as a
// whole, which is what the disabled buttons say.
func (h *handler) selected(c *echo.Context) ([]store.Server, error) {
	if err := c.Request().ParseForm(); err != nil {
		return nil, echo.ErrBadRequest
	}
	ctx := c.Request().Context()
	seen := map[int64]bool{}
	var out []store.Server
	for _, raw := range c.Request().Form["server"] {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		s, err := store.GetServer(ctx, h.Store.DB.R, id)
		if err != nil {
			return nil, notFound(err)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, echo.NewHTTPError(http.StatusUnprocessableEntity, "select at least one server")
	}
	for _, s := range out {
		if s.State != "active" || s.Retiring() {
			return nil, echo.NewHTTPError(http.StatusConflict, s.Name+" is not active: the bulk actions take active servers only")
		}
	}
	return out, nil
}

func (h *handler) bulkChecks(c *echo.Context) error {
	servers, err := h.selected(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	n := 0
	for _, s := range servers {
		res, err := h.Health.RunNow(ctx, s.ID, "admin")
		if err != nil {
			return err
		}
		if !res.Busy {
			n++
		}
	}
	return web.Redirect(c, "/servers?bulk=checks&n="+strconv.Itoa(n))
}

type bulkPauseView struct {
	Servers []store.Server
	Options []pauseOption
	Err     string
}

// bulkPause asks how long (no "d" posted yet) or pauses every selected server.
func (h *handler) bulkPause(c *echo.Context) error {
	servers, err := h.selected(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := bulkPauseView{Servers: servers, Options: h.pauseOptions(ctx)}
	d, forever, ok := parsePause(c.FormValue("d"))
	if !ok {
		if c.FormValue("d") != "" {
			v.Err = i18n.T(ctx, "health.pause.err")
		}
		status := http.StatusOK
		if v.Err != "" {
			status = http.StatusUnprocessableEntity
		}
		return web.Render(c, status, bulkPausePage(h.shell(c, i18n.T(ctx, "servers.bulk.pause_title"), "/servers"), v))
	}
	ids := make([]int64, len(servers))
	for i, s := range servers {
		ids[i] = s.ID
	}
	if err := h.Health.Pause(ctx, ids, d, forever, "admin"); err != nil {
		return err
	}
	return web.Redirect(c, "/servers?bulk=pause&n="+strconv.Itoa(len(ids)))
}

// parsePause reads the pause dialog's choice.
func parsePause(v string) (d time.Duration, untilResumed, ok bool) {
	switch v {
	case "1h":
		return time.Hour, false, true
	case "6h":
		return 6 * time.Hour, false, true
	case "24h":
		return 24 * time.Hour, false, true
	case "resume":
		return 0, true, true
	}
	return 0, false, false
}

// rowState is what the bulk buttons look at: only an active server can be
// taken by Run checks, Upgrade and Pause.
func rowState(r serverRow) string {
	if r.Bulk {
		return "active"
	}
	return "other"
}

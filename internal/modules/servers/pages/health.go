package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) registerHealth(r web.Routes) {
	r.Admin.GET("/servers/:id/health", h.healthTab)
	r.Admin.POST("/servers/:id/checks/run", h.runChecks)
	r.Admin.GET("/servers/:id/pause", h.pauseForm)
	r.Admin.POST("/servers/:id/pause", h.pause)
	r.Admin.POST("/servers/:id/resume", h.resume)
	r.Admin.GET("/servers/:id/stats", h.statsTab)
}

// reasonText renders a stored reason ({"key","args"}) in the page's language.
func reasonText(ctx context.Context, raw string) string {
	r, ok := health.ParseReason(raw)
	if !ok {
		return ""
	}
	return health.RenderReason(i18n.From(ctx), r)
}

// healthKind maps a health state to the marker kind.
func healthKind(state string) string {
	return map[string]string{"healthy": "ok", "degraded": "look", "blocked": "blocked", "down": "broken", "unknown": "unknown", "paused": "paused"}[state]
}

// ---------------------------------------------------------------- the tab

type matrixRow struct {
	Group      string // a group header row
	Label, Sub string
	Kind       string // marker kind of the result
	Result     string
	Detail     string
	When, At   string
	Strip      []string
	StripLabel string
	Indent     bool
}

type changeRow struct{ Time, From, To, Reason, ToKind string }

type healthView struct {
	serverView
	Health         string
	HealthKind     string
	Since, SinceAt string
	Reason         string
	// Pending: the latest evaluation disagrees with the state and waits for
	// confirmation.
	Pending      string
	Home         string
	HomeKind     string
	HomeNote     string
	HomeSince    string
	Frozen       bool
	Paused       bool
	PausedUntil  string
	Matrix       []matrixRow
	Ranges       []ui.Chip
	Range        string
	Segments     []ui.Segment
	TimelineFrom time.Time
	TimelineTo   time.Time
	Legend       []legendRow
	Axis         []string
	Changes      []changeRow
	Msg          string // queued busy over_budget
	PollHref     string // set while the tab polls for fresh results
	Ago          func(time.Time) string
}

type legendRow struct{ Kind, Word, Duration string }

func (h *handler) healthTab(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	if s.State != "active" {
		return web.Redirect(c, serverHref(s.ID))
	}
	ctx := c.Request().Context()
	v, err := h.buildHealth(ctx, s, c.QueryParam("range"))
	if err != nil {
		return err
	}
	v.Msg = c.QueryParam("msg")
	if started, err := strconv.ParseInt(c.QueryParam("run"), 10, 64); err == nil && h.Health.Now().Unix()-started < 60 {
		v.PollHref = serverHref(s.ID) + "/health?range=" + v.Range + "&run=" + strconv.FormatInt(started, 10)
	}
	return web.Render(c, http.StatusOK, healthPage(h.shell(c, s.Name+" · "+i18n.T(ctx, "servers.tab.health"), "/servers"), v))
}

func (h *handler) buildHealth(ctx context.Context, s store.Server, rangeParam string) (healthView, error) {
	loc := i18n.From(ctx)
	now := h.Health.Now()
	v := healthView{serverView: h.headView(ctx, s), Ago: loc.Ago}
	hr, err := store.GetHealth(ctx, h.Store.DB.R, s.ID)
	if err != nil {
		return v, err
	}
	detail := health.ParseDetail(hr.Detail)
	v.Health, v.HealthKind = hr.Health, healthKind(hr.Health)
	v.Reason = reasonText(ctx, hr.Reason)
	if !hr.Since.IsZero() {
		v.Since, v.SinceAt = loc.Ago(hr.Since.Time), loc.Time(hr.Since.Time)
	}
	if detail.Candidate != "" && detail.Candidate != hr.Health && detail.Candidate != health.Paused && hr.CandidateCount > 0 {
		v.Pending = i18n.T(ctx, "health.pending", i18n.Args{
			"state": i18n.T(ctx, "servers.health."+detail.Candidate), "reason": health.RenderReason(loc, detail.Reason), "n": hr.CandidateCount,
		})
	}
	v.Paused = !hr.PausedUntil.IsZero()
	if v.Paused && hr.PausedUntil.Before(store.PausedForever.Time) {
		v.PausedUntil = loc.Time(hr.PausedUntil.Time)
	}

	home, homeSince, err := store.GetHome(ctx, h.Store.DB.R)
	if err != nil {
		return v, err
	}
	v.Home, v.HomeKind = home, map[string]string{store.HomeOnline: "ok", store.HomeForeignUnreachable: "look", store.HomeOffline: "broken"}[home]
	v.Frozen = home != store.HomeOnline
	if v.Frozen {
		v.HomeNote = i18n.T(ctx, "health.frozen."+home)
		if !homeSince.IsZero() {
			v.HomeSince = loc.Ago(homeSince.Time)
		}
	}

	if err := h.buildMatrix(ctx, s, now, &v); err != nil {
		return v, err
	}
	return v, h.buildTimeline(ctx, s, hr, now, rangeParam, &v)
}

func formatMS(ms int) string { return strconv.Itoa(ms) + " ms" }

func (h *handler) buildMatrix(ctx context.Context, s store.Server, now time.Time, v *healthView) error {
	loc := i18n.From(ctx)
	q := h.Store.DB.R
	cfg, err := h.Health.Config(ctx)
	if err != nil {
		return err
	}
	since := db.At(now.Add(-24 * time.Hour))
	group := func(key string, args ...i18n.Args) {
		v.Matrix = append(v.Matrix, matrixRow{Group: i18n.T(ctx, key, args...)})
	}
	when := func(t db.Time) (string, string) { return loc.Ago(t.Time), loc.Time(t.Time) }
	stale := func(t db.Time, every time.Duration) bool { return t.Before(now.Add(-2 * every)) }
	kindOf := func(ok bool, t db.Time, every time.Duration, inconclusive bool) string {
		switch {
		case inconclusive || stale(t, every):
			return "unknown"
		case ok:
			return "ok"
		}
		return "broken"
	}

	// Home internet.
	group("health.m.home")
	refs, err := store.RecentReferences(ctx, q, 1)
	if err != nil {
		return err
	}
	row := matrixRow{Label: i18n.T(ctx, "health.m.reference"), Kind: v.HomeKind, Result: i18n.T(ctx, "health.home."+v.Home)}
	if len(refs) > 0 {
		row.When, row.At = when(refs[0].At)
		var d health.RefDetail
		_ = json.Unmarshal([]byte(refs[0].Detail), &d)
		row.Detail = i18n.T(ctx, "health.m.reference_detail", i18n.Args{"dom": okCount(d.Domestic), "dom_n": len(d.Domestic), "for": okCount(d.Foreign), "for_n": len(d.Foreign)})
	}
	v.Matrix = append(v.Matrix, row)

	// The self-check and each check of the template.
	group("health.m.self")
	selfs, err := store.LatestResults(ctx, q, s.ID, "self")
	if err != nil {
		return err
	}
	if len(selfs) == 0 {
		v.Matrix = append(v.Matrix, matrixRow{Label: i18n.T(ctx, "health.m.none"), Kind: "unknown", Result: i18n.T(ctx, "health.m.no_result")})
	}
	selfHist, err := store.ResultsSince(ctx, q, s.ID, "self", since)
	if err != nil {
		return err
	}
	for _, r := range selfs {
		w, at := when(r.At)
		var d struct {
			Checks []struct {
				Kind    string `json:"kind"`
				Pass    bool   `json:"pass"`
				Message string `json:"message"`
			} `json:"checks"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal([]byte(r.Detail), &d)
		kind := kindOf(r.OK, r.At, cfg.SelfEvery, r.Inconclusive)
		detail := d.Error
		v.Matrix = append(v.Matrix, matrixRow{
			Label: i18n.T(ctx, "health.m.self_check"), Kind: kind, Result: i18n.T(ctx, "health.self."+r.Class), Detail: detail, When: w, At: at,
			Strip: strip(selfHist, "", "", now, 48, 30*time.Minute), StripLabel: i18n.T(ctx, "health.m.strip"),
		})
		for _, ch := range d.Checks {
			k := "ok"
			if !ch.Pass {
				k = "broken"
			}
			v.Matrix = append(v.Matrix, matrixRow{Label: ch.Kind, Kind: k, Result: i18n.T(ctx, "health.m.check_"+k), Detail: ch.Message, Indent: true})
		}
	}

	// The proxy test per endpoint.
	group("health.m.proxy")
	eps, err := store.Endpoints(ctx, q, s.ID)
	if err != nil {
		return err
	}
	proxies, err := store.LatestResults(ctx, q, s.ID, "proxy")
	if err != nil {
		return err
	}
	proxyHist, err := store.ResultsSince(ctx, q, s.ID, "proxy", since)
	if err != nil {
		return err
	}
	byKey := map[string]store.CheckResult{}
	for _, r := range proxies {
		byKey[r.EndpointKey] = r
	}
	for _, e := range eps {
		r, ok := byKey[e.Key]
		label := e.DisplayName
		if label == "" {
			label = e.Key
		}
		if !ok {
			v.Matrix = append(v.Matrix, matrixRow{Label: label, Sub: e.Key, Kind: "unknown", Result: i18n.T(ctx, "health.m.no_result")})
			continue
		}
		var d struct {
			ConnectMS   int    `json:"connect_ms"`
			FirstByteMS int    `json:"first_byte_ms"`
			Kbps        int    `json:"kbps"`
			Error       string `json:"error"`
		}
		_ = json.Unmarshal([]byte(r.Detail), &d)
		w, at := when(r.At)
		row := matrixRow{Label: label, Sub: e.Key, Kind: kindOf(r.OK, r.At, cfg.SelfEvery, r.Inconclusive), When: w, At: at,
			Strip: strip(proxyHist, e.Key, "", now, 48, 30*time.Minute), StripLabel: i18n.T(ctx, "health.m.strip")}
		if r.OK {
			row.Result = i18n.T(ctx, "health.m.ok")
			row.Detail = i18n.T(ctx, "health.m.proxy_detail", i18n.Args{"connect": d.ConnectMS, "first": d.FirstByteMS, "kbps": d.Kbps})
		} else {
			row.Result = r.Class
			row.Detail = d.Error
		}
		if r.Inconclusive {
			row.Detail = strings.TrimSpace(row.Detail + " " + i18n.T(ctx, "health.m.inconclusive"))
		}
		v.Matrix = append(v.Matrix, row)
	}

	// Each node of check-host.
	ext, err := store.LatestResults(ctx, q, s.ID, "external")
	if err != nil {
		return err
	}
	extHist, err := store.ResultsSince(ctx, q, s.ID, "external", since)
	if err != nil {
		return err
	}
	var newest db.Time
	for _, r := range ext {
		if r.At.After(newest.Time) {
			newest = r.At
		}
	}
	known, _ := store.CheckhostNodes(ctx, q)
	cities := map[string]string{}
	for _, n := range known {
		cities[n.Name] = n.City
	}
	byRegion := map[string][]store.CheckResult{}
	var meta *store.CheckResult
	for i, r := range ext {
		if !r.At.Equal(newest.Time) {
			continue
		}
		if r.Vantage == "check-host" {
			meta = &ext[i]
			continue
		}
		var d struct {
			Region string `json:"region"`
		}
		_ = json.Unmarshal([]byte(r.Detail), &d)
		byRegion[d.Region] = append(byRegion[d.Region], r)
	}
	nodeRows := func(region string) []matrixRow {
		list := byRegion[region]
		sort.Slice(list, func(i, j int) bool { return list[i].Vantage < list[j].Vantage })
		var rows []matrixRow
		for _, r := range list {
			var d struct {
				Country string `json:"country"`
				MS      int    `json:"ms"`
				Error   string `json:"error"`
			}
			_ = json.Unmarshal([]byte(r.Detail), &d)
			w, at := when(r.At)
			name := cities[r.Vantage]
			if name == "" {
				name = strings.TrimSuffix(r.Vantage, ".node.check-host.net")
			}
			row := matrixRow{Label: name, Sub: strings.TrimSuffix(r.Vantage, ".node.check-host.net"), Kind: kindOf(r.OK, r.At, cfg.ExternalEvery, r.Inconclusive), When: w, At: at,
				Strip: strip(extHist, "", r.Vantage, now, 48, 30*time.Minute), StripLabel: i18n.T(ctx, "health.m.strip")}
			if r.OK {
				row.Result, row.Detail = i18n.T(ctx, "health.m.connected"), formatMS(d.MS)
			} else {
				row.Result, row.Detail = r.Class, d.Error
			}
			rows = append(rows, row)
		}
		return rows
	}
	ru, abroad := nodeRows("ru"), nodeRows("abroad")
	group("health.m.ru")
	v.Matrix = append(v.Matrix, ru...)
	group("health.m.abroad")
	v.Matrix = append(v.Matrix, abroad...)
	if len(ru)+len(abroad) == 0 {
		row := matrixRow{Label: "check-host.net", Kind: "unknown", Result: i18n.T(ctx, "health.m.no_result")}
		if meta != nil {
			var d struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal([]byte(meta.Detail), &d)
			row.Result, row.Detail = i18n.T(ctx, "health.ext."+meta.Class), d.Reason
			row.When, row.At = when(meta.At)
		}
		v.Matrix = append(v.Matrix, row)
	}
	return nil
}

func okCount(ps []health.RefProbe) int {
	n := 0
	for _, p := range ps {
		if p.OK {
			n++
		}
	}
	return n
}

// strip folds results into cells of step each, ending now: ok when a cell has
// only passes, broken when it has a failure, none without results.
func strip(rs []store.CheckResult, endpoint, vantage string, now time.Time, cells int, step time.Duration) []string {
	out := make([]string, cells)
	for i := range out {
		out[i] = ui.CellNone
	}
	start := now.Add(-time.Duration(cells) * step)
	for _, r := range rs {
		if (endpoint != "" && r.EndpointKey != endpoint) || (vantage != "" && r.Vantage != vantage) || r.Inconclusive || r.Class == "skipped" || r.Class == "unavailable" {
			continue
		}
		i := int(r.At.Sub(start) / step)
		if i < 0 || i >= cells {
			continue
		}
		switch {
		case !r.OK:
			out[i] = ui.CellFail
		case out[i] == ui.CellNone:
			out[i] = ui.CellOK
		}
	}
	return out
}

// ----------------------------------------------------------- the timeline

func (h *handler) buildTimeline(ctx context.Context, s store.Server, hr store.Health, now time.Time, rangeParam string, v *healthView) error {
	loc := i18n.From(ctx)
	span := map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	rng := rangeParam
	if _, ok := span[rng]; !ok {
		rng = "24h"
	}
	v.Range = rng
	for _, r := range []string{"24h", "7d", "30d"} {
		v.Ranges = append(v.Ranges, ui.Chip{Label: i18n.T(ctx, "health.range."+r), Href: serverHref(s.ID) + "/health?range=" + r, On: r == rng})
	}
	from, to := now.Add(-span[rng]), now
	if !s.ActivatedAt.IsZero() && s.ActivatedAt.After(from) {
		from = s.ActivatedAt.Time
	}
	v.TimelineFrom, v.TimelineTo = from, to

	list, err := events.List(ctx, h.Store.DB.R, events.Filter{Type: "server.health_changed", Subject: store.ServerSubject(s.ID), Limit: 500})
	if err != nil {
		return err
	}
	// oldest first
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	type change struct {
		at       time.Time
		from, to string
		reason   string
	}
	var changes []change
	for _, e := range list {
		f, _ := e.Payload["from"].(string)
		t, _ := e.Payload["to"].(string)
		r, _ := e.Payload["reason"].(string)
		if key, _ := e.Payload["reason_key"].(string); key != "" {
			args, _ := e.Payload["reason_args"].(map[string]any)
			r = health.RenderReason(loc, health.Reason{Key: key, Args: args})
		}
		changes = append(changes, change{e.Time.Time, f, t, r})
	}
	// the state at the start of the range is the one the first change left
	state := hr.Health
	for _, c := range changes {
		if c.at.After(from) {
			state = c.from
			break
		}
	}
	cur, at := state, from
	dur := map[string]time.Duration{}
	push := func(kind string, a, b time.Time) {
		if !b.After(a) {
			return
		}
		dur[kind] += b.Sub(a)
		word := i18n.T(ctx, "servers.health."+kind)
		v.Segments = append(v.Segments, ui.Segment{Kind: healthKind(kind), From: a, To: b,
			Title: word + " · " + loc.Time(a) + " – " + loc.Time(b)})
	}
	for _, c := range changes {
		if !c.at.After(from) {
			continue
		}
		push(cur, at, c.at)
		cur, at = c.to, c.at
	}
	push(cur, at, to)
	order := []string{"healthy", "degraded", "blocked", "down", "paused", "unknown"}
	for _, k := range order {
		if dur[k] > 0 {
			v.Legend = append(v.Legend, legendRow{Kind: healthKind(k), Word: i18n.T(ctx, "servers.health."+k), Duration: loc.Duration(dur[k].Round(time.Minute))})
		}
	}
	layout := "15:04"
	if rng != "24h" {
		layout = "2 Jan"
	}
	for i := 0; i <= 4; i++ {
		t := from.Add(to.Sub(from) * time.Duration(i) / 4)
		v.Axis = append(v.Axis, t.In(loc.TZ).Format(layout))
	}
	// the list under the bar, newest first
	for i := len(changes) - 1; i >= 0; i-- {
		c := changes[i]
		if c.at.Before(from) {
			continue
		}
		v.Changes = append(v.Changes, changeRow{Time: loc.Time(c.at), From: i18n.T(ctx, "servers.health."+c.from), To: i18n.T(ctx, "servers.health."+c.to), Reason: c.reason, ToKind: healthKind(c.to)})
	}
	return nil
}

// --------------------------------------------------------------- actions

func (h *handler) runChecks(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	res, err := h.Health.RunNow(c.Request().Context(), s.ID, "admin")
	if errors.Is(err, health.ErrNotActive) {
		return web.Redirect(c, serverHref(s.ID))
	}
	if err != nil {
		return err
	}
	msg := "queued"
	switch {
	case res.Busy:
		msg = "busy"
	case res.OverBudget:
		msg = "over_budget"
	}
	run := ""
	if !res.Busy {
		run = "&run=" + strconv.FormatInt(h.Health.Now().Unix(), 10)
	}
	return web.Redirect(c, serverHref(s.ID)+"/health?msg="+msg+run)
}

type pauseView struct {
	S       store.Server
	Options []pauseOption
	Health  string
	Since   string
	Every   string
	Err     string
}

type pauseOption struct{ Value, Label string }

func (h *handler) pauseOptions(ctx context.Context) []pauseOption {
	return []pauseOption{
		{"1h", i18n.T(ctx, "health.pause.1h")}, {"6h", i18n.T(ctx, "health.pause.6h")},
		{"24h", i18n.T(ctx, "health.pause.24h")}, {"resume", i18n.T(ctx, "health.pause.until_resumed")},
	}
}

func (h *handler) pauseView(ctx context.Context, s store.Server) pauseView {
	loc := i18n.From(ctx)
	v := pauseView{S: s, Options: h.pauseOptions(ctx), Health: i18n.T(ctx, "servers.health."+s.Health)}
	if !s.HealthSince.IsZero() {
		v.Since = loc.Time(s.HealthSince.Time)
	}
	if every, err := h.Settings.GetDuration(ctx, conf.SelfcheckEvery); err == nil {
		v.Every = loc.Duration(every)
	}
	return v
}

// pauseForm answers the dialog (to htmx) or a page with the same form.
func (h *handler) pauseForm(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := h.pauseView(ctx, s)
	if c.Request().Header.Get("HX-Request") != "" {
		return web.Render(c, http.StatusOK, pauseDialog(v))
	}
	return web.Render(c, http.StatusOK, pausePage(h.shell(c, i18n.T(ctx, "health.pause.title", i18n.Args{"name": s.Name}), "/servers"), v))
}

func (h *handler) pause(c *echo.Context) error {
	s, err := h.activeServer(c)
	if err != nil {
		return err
	}
	var d time.Duration
	forever := false
	switch c.FormValue("d") {
	case "1h":
		d = time.Hour
	case "6h":
		d = 6 * time.Hour
	case "24h":
		d = 24 * time.Hour
	case "resume":
		forever = true
	default:
		v := h.pauseView(c.Request().Context(), s)
		v.Err = i18n.T(c.Request().Context(), "health.pause.err")
		return web.Render(c, http.StatusUnprocessableEntity, pausePage(h.shell(c, i18n.T(c.Request().Context(), "health.pause.title", i18n.Args{"name": s.Name}), "/servers"), v))
	}
	if err := h.Health.Pause(c.Request().Context(), []int64{s.ID}, d, forever, "admin"); err != nil {
		return err
	}
	return web.Redirect(c, serverHref(s.ID)+"/health")
}

func (h *handler) resume(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	if err := h.Health.Resume(c.Request().Context(), s.ID, "admin"); err != nil {
		return err
	}
	return web.Redirect(c, serverHref(s.ID)+"/health?run="+strconv.FormatInt(h.Health.Now().Unix(), 10))
}

// flag is a server's flag with its name.
func flagName(s store.Server) string { return country.Flag(s.Country) + " " + s.Name }

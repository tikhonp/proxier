package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const routersPath = "/routing/routers"

func routerHref(id int64) string { return routersPath + "/" + i64(id) }

// registerRouters adds the routers' routes (3e).
func (h *handler) registerRouters(r web.Routes) {
	r.Admin.GET(routersPath, h.routersList)
	r.Admin.GET(routersPath+"/new", h.routerAddPage)
	r.Admin.POST(routersPath+"/test", h.routerTest)
	r.Admin.GET(routersPath+"/tests/:test", h.routerTestArea)
	r.Admin.POST(routersPath+"/tests/:test/confirm", h.routerConfirm)
	r.Admin.POST(routersPath, h.routerCreate)
	r.Admin.GET(routersPath+"/:id", h.routerPage)
	r.Admin.POST(routersPath+"/:id/sync", h.routerSyncNow)
	r.Admin.POST(routersPath+"/:id/preview", h.routerPreviewStart)
	r.Admin.GET(routersPath+"/:id/preview", h.routerPreviewArea)
	r.Admin.GET(routersPath+"/:id/syncs/:sync", h.routerSyncPage)
	r.Admin.GET(routersPath+"/:id/syncs/:sync/script.rsc", h.routerScript)
	r.Admin.GET(routersPath+"/:id/edit", h.routerEditPage)
	r.Admin.POST(routersPath+"/:id/edit", h.routerEdit)
	r.Admin.GET(routersPath+"/:id/list", h.routerListPage)
	r.Admin.POST(routersPath+"/:id/list", h.routerSetList)
}

func (h *handler) loadRouter(c *echo.Context) (routers.Router, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return routers.Router{}, echo.ErrNotFound
	}
	r, err := h.Routers.Get(c.Request().Context(), id)
	if errors.Is(err, routers.ErrNotFound) {
		return r, echo.ErrNotFound
	}
	return r, err
}

// ------------------------------------------------------------------ status words

// statusKind is the marker of a sync status.
func statusKind(st string) string {
	switch st {
	case routers.StatusSynced:
		return "ok"
	case routers.StatusSyncing:
		return "running"
	case routers.StatusFailed:
		return "broken"
	case routers.StatusUntested:
		return "look"
	}
	return "unknown"
}

// routerWord is a router's sync word for targets: "synced 1 h ago",
// "syncing", "sync failed · connect", "waits 30 s", "never synced".
func (h *handler) routerWord(ctx context.Context, r routers.Router) (kind, word string, err error) {
	loc := i18n.From(ctx)
	if r.State != "active" {
		return "off", i18n.T(ctx, "routers.state."+r.State), nil
	}
	busy, err := h.Routers.Busy(ctx, r.ID)
	if err != nil {
		return "", "", err
	}
	st := routers.StatusOf(r, busy.Syncing)
	kind = statusKind(st)
	switch {
	case busy.Syncing:
		return kind, i18n.T(ctx, "routers.word.syncing"), nil
	case busy.Queued && !busy.Retry && busy.StartAt.After(h.Now()):
		return "running", i18n.T(ctx, "routers.word.waits", i18n.Args{"in": loc.Duration(busy.StartAt.Sub(h.Now()).Round(time.Second))}), nil
	}
	switch st {
	case routers.StatusSynced:
		return kind, i18n.T(ctx, "routers.word.synced", i18n.Args{"ago": loc.Ago(r.LastSyncAt)}), nil
	case routers.StatusFailed:
		step := ""
		if f, ok, err := h.Routers.LastFailure(ctx, r.ID); err != nil {
			return "", "", err
		} else if ok {
			step = f.Step
		}
		return kind, i18n.T(ctx, "routers.word.failed", i18n.Args{"step": step}), nil
	case routers.StatusUntested:
		return kind, i18n.T(ctx, "routers.word.untested"), nil
	}
	return kind, i18n.T(ctx, "routers.word.never"), nil
}

// ------------------------------------------------------------------ the routers page

type routerRow struct {
	ID                      int64
	Name, Addr, State, List string
	Kind, Word, Detail      string
}

func (h *handler) routersList(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	rs, err := h.Routers.List(ctx)
	if err != nil {
		return err
	}
	rows := make([]routerRow, 0, len(rs))
	for _, r := range rs {
		row := routerRow{ID: r.ID, Name: r.Name, List: r.List, State: i18n.T(ctx, "routers.state."+r.State), Addr: addrLine(ctx, r)}
		if row.Kind, row.Word, err = h.routerWord(ctx, r); err != nil {
			return err
		}
		var parts []string
		if !r.LastSyncAt.IsZero() {
			parts = append(parts, loc.Ago(r.LastSyncAt))
		}
		switch r.LastResult {
		case "failed":
			parts = append(parts, r.LastError)
			busy, err := h.Routers.Busy(ctx, r.ID)
			if err != nil {
				return err
			}
			if busy.Retry {
				parts = append(parts, i18n.T(ctx, "routers.retry_at", i18n.Args{"time": loc.Clock(busy.StartAt)}))
			}
		case "synced":
			if s, ok, err := h.lastPushed(ctx, r.ID); err != nil {
				return err
			} else if ok {
				parts = append(parts, s)
			}
		}
		row.Detail = strings.Join(parts, " · ")
		rows = append(rows, row)
	}
	return web.Render(c, http.StatusOK, routersPage(h.shell(c, i18n.T(ctx, "routers.title"), routersPath), rows))
}

// lastPushed is "1 tag updated" for the newest sync that did something.
func (h *handler) lastPushed(ctx context.Context, id int64) (string, bool, error) {
	ss, err := h.Routers.Syncs(ctx, id, 0, 1)
	if err != nil || len(ss) == 0 || ss[0].Kind != "sync" || ss[0].State != "done" {
		return "", false, err
	}
	return syncSummary(ctx, ss[0]), true, nil
}

// addrLine is "10.230.2.1 · via tailnet jump host" or "10.230.1.1 · RB5009 · 7.24.5".
func addrLine(ctx context.Context, r routers.Router) string {
	parts := []string{r.Conn.Host}
	switch {
	case r.Conn.JumpHost != "" && r.Conn.Tailnet:
		parts = append(parts, i18n.T(ctx, "routers.via_tailnet_jump", i18n.Args{"jump": r.Conn.JumpHost}))
	case r.Conn.JumpHost != "":
		parts = append(parts, i18n.T(ctx, "routers.via_jump", i18n.Args{"jump": r.Conn.JumpHost}))
	case r.Conn.Tailnet:
		parts = append(parts, i18n.T(ctx, "routers.via_tailnet"))
	}
	if r.Board != "" {
		parts = append(parts, r.Board)
	}
	if r.Version != "" {
		parts = append(parts, strings.TrimSuffix(r.Version, " (stable)"))
	}
	return strings.Join(parts, " · ")
}

// syncSummary is "1 update · 9 s" (+ " · after linkedin changed upstream" in history).
func syncSummary(ctx context.Context, s routers.Sync) string {
	loc := i18n.From(ctx)
	var parts []string
	if s.Added > 0 {
		parts = append(parts, loc.N("routers.sum.added", int64(s.Added)))
	}
	if s.Updated > 0 {
		parts = append(parts, loc.N("routers.sum.updated", int64(s.Updated)))
	}
	if s.Removed > 0 {
		parts = append(parts, loc.N("routers.sum.removed", int64(s.Removed)))
	}
	if s.Recorded > 0 {
		parts = append(parts, loc.N("routers.sum.recorded", int64(s.Recorded)))
	}
	if len(parts) == 0 {
		parts = append(parts, i18n.T(ctx, "routers.sum.nothing"))
	}
	return strings.Join(parts, " · ")
}

// ------------------------------------------------------------------ add router, tests

// routerForm is the add and edit form's values.
type routerForm struct {
	Name     string
	ListID   int64
	Conn     routers.Connection
	FirstHop string // direct, tailnet
}

func formConn(c *echo.Context) routers.Connection {
	port, _ := strconv.Atoi(strings.TrimSpace(c.FormValue("port")))
	jport, _ := strconv.Atoi(strings.TrimSpace(c.FormValue("jump_port")))
	return routers.Connection{
		Host: c.FormValue("host"), Port: port, User: c.FormValue("user"),
		JumpHost: c.FormValue("jump_host"), JumpPort: jport, JumpUser: c.FormValue("jump_user"),
		Tailnet: c.FormValue("first_hop") == "tailnet",
		Names:   routeros.Names{List: c.FormValue("list_name"), Forwarder: c.FormValue("forwarder")},
	}
}

type rtAddView struct {
	F       routerForm
	Lists   []lists.List
	Key     string // Proxier's public key, authorized_keys form
	Errs    map[string]string
	Test    *testView
	TestID  int64
	Tailnet bool // the node runs
}

func (h *handler) rtRenderAdd(c *echo.Context, status int, v rtAddView) error {
	ctx := c.Request().Context()
	all, err := h.Lists.All(ctx)
	if err != nil {
		return err
	}
	v.Lists = all
	if key, _, err := h.SSH.PublicKey(ctx); err == nil {
		v.Key = strings.TrimSpace(key)
	}
	v.Tailnet = h.Routers.TailnetRunning(ctx)
	return web.Render(c, status, routerAddPage(h.shell(c, i18n.T(ctx, "routers.add.title"), routersPath), v))
}

func (h *handler) routerAddPage(c *echo.Context) error {
	ctx := c.Request().Context()
	def, err := h.Lists.Default(ctx)
	if err != nil {
		return err
	}
	v := rtAddView{F: routerForm{ListID: def.ID, Conn: routers.Connection{}.Normalize(), FirstHop: "direct"}}
	if h.Routers.TailnetRunning(ctx) {
		v.F.FirstHop = "tailnet"
	}
	v.F.Conn.Host = ""
	if id := formID(c.QueryParam("test")); id != 0 {
		t, err := h.Routers.Test(ctx, id)
		if err == nil && t.RouterID == 0 {
			v.F.Conn, v.TestID = t.Conn, t.ID
			v.F.FirstHop = hopOf(t.Conn)
			tv := h.testView(ctx, t, c.QueryParam("name"), formID(c.QueryParam("list")))
			v.Test = &tv
		}
	}
	if n := c.QueryParam("name"); n != "" {
		v.F.Name = n
	}
	if l := formID(c.QueryParam("list")); l != 0 {
		v.F.ListID = l
	}
	return h.rtRenderAdd(c, http.StatusOK, v)
}

func hopOf(c routers.Connection) string {
	if c.Tailnet {
		return "tailnet"
	}
	return "direct"
}

// routerTest starts Test connection of the posted form, or of a saved
// router (router=<id>), and goes back to the page that shows it.
func (h *handler) routerTest(c *echo.Context) error {
	ctx := c.Request().Context()
	rid := formID(c.FormValue("router"))
	conn := formConn(c)
	if rid != 0 {
		r, err := h.Routers.Get(ctx, rid)
		if errors.Is(err, routers.ErrNotFound) {
			return echo.ErrNotFound
		} else if err != nil {
			return err
		}
		conn = r.Conn
	}
	id, err := h.Routers.StartTest(ctx, rid, conn, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		return h.rtRenderAdd(c, http.StatusUnprocessableEntity, rtAddView{
			F:    routerForm{Name: c.FormValue("name"), ListID: formID(c.FormValue("list")), Conn: conn, FirstHop: c.FormValue("first_hop")},
			Errs: errText(c.Request().Context(), fe),
		})
	}
	if err != nil {
		return err
	}
	if rid != 0 {
		return web.Redirect(c, routerHref(rid)+"?test="+i64(id)+"#connection")
	}
	return web.Redirect(c, testReturn(id, c.FormValue("name"), formID(c.FormValue("list"))))
}

// testReturn is the add page showing a test with the form's name and list.
func testReturn(id int64, name string, list int64) string {
	q := "?test=" + i64(id)
	if name != "" {
		q += "&name=" + urlQuery(name)
	}
	if list != 0 {
		q += "&list=" + i64(list)
	}
	return routersPath + "/new" + q
}

func urlQuery(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B", " ", "+", "?", "%3F").Replace(s)
}

type checkRow struct {
	Name, Detail string
	OK, Warn     bool
}

type testView struct {
	ID, RouterID int64
	State        string
	Running      bool
	Checks       []checkRow
	Hop          string // the hop to confirm, worded
	Addr, FP     string
	Error        string
	Name         string // the add form's name and list, kept through confirmations
	ListID       int64
	PollHref     string
}

func (h *handler) testView(ctx context.Context, t routers.Test, name string, list int64) testView {
	v := testView{ID: t.ID, RouterID: t.RouterID, State: t.State, Running: t.State == routers.TestRunning, Addr: t.ConfirmAddr,
		FP: t.ConfirmFP, Error: t.Error, Name: name, ListID: list}
	if t.ConfirmHop != "" {
		v.Hop = i18n.T(ctx, "routers.test.hop."+t.ConfirmHop)
	}
	for _, ch := range t.Checks {
		v.Checks = append(v.Checks, checkRow{
			Name:   i18n.T(ctx, ch.Name, i18n.Args{"forwarder": t.Conn.Names.Forwarder, "list": t.Conn.Names.List}),
			Detail: ch.Detail, OK: ch.OK, Warn: ch.Warn,
		})
	}
	v.PollHref = routersPath + "/tests/" + i64(t.ID)
	if name != "" || list != 0 {
		v.PollHref += "?name=" + urlQuery(name) + "&list=" + i64(list)
	}
	return v
}

func (h *handler) loadTest(c *echo.Context) (routers.Test, error) {
	id, err := strconv.ParseInt(c.Param("test"), 10, 64)
	if err != nil {
		return routers.Test{}, echo.ErrNotFound
	}
	t, err := h.Routers.Test(c.Request().Context(), id)
	if errors.Is(err, routers.ErrTestNotFound) {
		return t, echo.ErrNotFound
	}
	return t, err
}

// routerTestArea is the test area, which polls itself while the test runs.
func (h *handler) routerTestArea(c *echo.Context) error {
	t, err := h.loadTest(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if !htmx(c) {
		if t.RouterID != 0 {
			return web.Redirect(c, routerHref(t.RouterID)+"?test="+i64(t.ID)+"#connection")
		}
		return web.Redirect(c, testReturn(t.ID, c.QueryParam("name"), formID(c.QueryParam("list"))))
	}
	return web.Render(c, http.StatusOK, testArea(h.testView(ctx, t, c.QueryParam("name"), formID(c.QueryParam("list")))))
}

// routerConfirm is It matches, continue (decision=trust) or Stop.
func (h *handler) routerConfirm(c *echo.Context) error {
	t, err := h.loadTest(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	next, err := h.Routers.Confirm(ctx, t.ID, c.FormValue("decision") == "trust", events.ActorAdmin)
	if errors.Is(err, routers.ErrNotConfirming) {
		next = 0
	} else if err != nil {
		return err
	}
	if next == 0 {
		next = t.ID
	}
	if t.RouterID != 0 {
		return web.Redirect(c, routerHref(t.RouterID)+"?test="+i64(next)+"#connection")
	}
	return web.Redirect(c, testReturn(next, c.FormValue("name"), formID(c.FormValue("list"))))
}

// routerCreate is Save and sync: always allowed; with a passing test of
// these values the router is connected and its initial sync queued.
func (h *handler) routerCreate(c *echo.Context) error {
	ctx := c.Request().Context()
	f := routerForm{Name: c.FormValue("name"), ListID: formID(c.FormValue("list")), Conn: formConn(c), FirstHop: c.FormValue("first_hop")}
	testID := formID(c.FormValue("test"))
	id, err := h.Routers.Create(ctx, f.Name, f.ListID, f.Conn, testID, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v := rtAddView{F: f, Errs: errText(ctx, fe), TestID: testID}
		if testID != 0 {
			if t, err := h.Routers.Test(ctx, testID); err == nil {
				tv := h.testView(ctx, t, f.Name, f.ListID)
				v.Test = &tv
			}
		}
		return h.rtRenderAdd(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(id)+"?added=1")
}

// ------------------------------------------------------------------ the router page

type planRow struct {
	Tag, Service, Want, Have, Action, Why string
	Kind                                  string // marker
}

type rtPreviewView struct {
	RouterID        int64
	SyncID          int64
	Running, Failed bool
	Error           string
	Read, Counts    string
	Rows            []planRow
	Script          string
	ScriptHref      string
	HasPushes       bool
}

type historyRow struct {
	ID                int64
	Time, Kind, Word  string
	Marker, Detail    string
	PlanHref, JobHref string
}

type routerView struct {
	R         routers.Router
	Kind      string
	Word      string
	Line      string
	Band      *failBand
	Waiting   bool
	Notice    string
	Preview   *rtPreviewView
	Tags      []planRow
	TagsNote  string
	Conn      connView
	Test      *testView
	History   []historyRow
	OlderHref string
	Activity  []eventLine
}

type failBand struct {
	Problem, Count, Retry string
}

type connView struct {
	Router, Jump, FirstHop string
	RouterFP, JumpFP       string
	List, Forwarder        string
}

const historyPage = 20

func (h *handler) routerPage(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	v := routerView{R: r, Waiting: !r.Connected() && r.State == "active"}
	busy, err := h.Routers.Busy(ctx, r.ID)
	if err != nil {
		return err
	}
	st := routers.StatusOf(r, busy.Syncing)
	v.Kind, v.Word = statusKind(st), i18n.T(ctx, "routers.status."+st)
	if !r.LastSyncAt.IsZero() && !busy.Syncing {
		v.Word += " · " + loc.Ago(r.LastSyncAt)
	}
	size, err := h.Lists.Size(ctx, r.ListID)
	if err != nil {
		return err
	}
	view, err := h.Lists.View(ctx, r.ListID, nil)
	if err != nil {
		return err
	}
	parts := []string{
		i18n.T(ctx, "routers.state."+r.State),
		i18n.T(ctx, "routers.follows", i18n.Args{"list": r.List, "services": loc.N("routers.services", int64(size)), "domains": loc.Number(int64(view.Result.Count()))}),
	}
	if r.Version != "" {
		parts = append(parts, "RouterOS "+strings.TrimSuffix(r.Version, " (stable)"))
	}
	if r.Board != "" {
		parts = append(parts, r.Board)
	}
	v.Line = strings.Join(parts, " · ")
	if r.LastResult == "failed" {
		b := &failBand{Problem: r.LastError, Count: loc.N("routers.band.in_a_row", int64(r.Failures))}
		if busy.Retry {
			b.Retry = i18n.T(ctx, "routers.band.retry", i18n.Args{"time": loc.Clock(busy.StartAt)})
		}
		v.Band = b
	}
	switch {
	case c.QueryParam("added") == "1":
		v.Notice = i18n.T(ctx, "routers.band.added")
		if v.Waiting {
			v.Notice = ""
		}
	case c.QueryParam("synced") == "1":
		v.Notice = i18n.T(ctx, "routers.band.sync_queued")
	case c.QueryParam("saved") == "1":
		v.Notice = i18n.T(ctx, "routers.band.saved")
	}
	last, planned, err := h.Routers.LatestPlan(ctx, r.ID)
	if err != nil {
		return err
	}
	if planned {
		v.Tags = planRows(ctx, last.Plan)
		v.TagsNote = i18n.T(ctx, "routers.tags.note", i18n.Args{"time": loc.Time(last.ReadAt), "kind": i18n.T(ctx, "routers.kind."+last.Kind)})
	}
	// a preview shows for an hour, until closed or a sync read the router after it
	if p, ok, err := h.Routers.LatestPreview(ctx, r.ID); err != nil {
		return err
	} else if superseded := planned && last.Kind == "sync" && last.ID > p.ID; ok && c.QueryParam("closed") != i64(p.ID) && !superseded {
		pv := h.rtPreviewOf(ctx, r, p)
		v.Preview = &pv
	}
	if v.Conn, err = h.connOf(ctx, r); err != nil {
		return err
	}
	testID := formID(c.QueryParam("test"))
	if testID == 0 {
		if t, ok, err := h.Routers.LatestTest(ctx, r.ID); err != nil {
			return err
		} else if ok && (t.State == routers.TestRunning || t.State == routers.TestConfirm) {
			testID = t.ID
		}
	}
	if testID != 0 {
		if t, err := h.Routers.Test(ctx, testID); err == nil && t.RouterID == r.ID {
			tv := h.testView(ctx, t, "", 0)
			v.Test = &tv
		}
	}
	before := formID(c.QueryParam("before"))
	ss, err := h.Routers.Syncs(ctx, r.ID, before, historyPage+1)
	if err != nil {
		return err
	}
	if len(ss) > historyPage {
		ss = ss[:historyPage]
		v.OlderHref = routerHref(r.ID) + "?before=" + i64(ss[len(ss)-1].ID) + "#history"
	}
	for _, s := range ss {
		v.History = append(v.History, h.historyRow(ctx, r, s))
	}
	if v.Activity, err = h.routerActivity(ctx, r); err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, routerPage(h.shell(c, r.Name, routersPath), v))
}

func (h *handler) connOf(ctx context.Context, r routers.Router) (connView, error) {
	v := connView{
		Router:   fmt.Sprintf("%s@%s", r.Conn.User, r.Conn.Address()),
		FirstHop: i18n.T(ctx, "routers.first_hop."+hopOf(r.Conn)),
		List:     r.Conn.Names.List, Forwarder: r.Conn.Names.Forwarder,
		RouterFP: i18n.T(ctx, "routers.not_pinned"),
	}
	if k, err := h.SSH.KnownHost(ctx, r.Conn.Address()); err == nil {
		v.RouterFP = k.Fingerprint
	}
	if r.Conn.JumpHost != "" {
		v.Jump = fmt.Sprintf("%s@%s", r.Conn.JumpUser, r.Conn.JumpAddress())
		v.JumpFP = i18n.T(ctx, "routers.not_pinned")
		if k, err := h.SSH.KnownHost(ctx, r.Conn.JumpAddress()); err == nil {
			v.JumpFP = k.Fingerprint
		}
	}
	return v, nil
}

func planRows(ctx context.Context, plan []routers.TagPlan) []planRow {
	loc := i18n.From(ctx)
	out := make([]planRow, 0, len(plan))
	for _, p := range plan {
		row := planRow{Tag: p.Tag, Service: p.Service, Have: loc.Number(int64(p.Have)), Action: i18n.T(ctx, "routers.plan."+p.Action)}
		if row.Service == "" {
			row.Service = "—"
		}
		row.Want = "—"
		if p.Want >= 0 {
			row.Want = loc.Number(int64(p.Want))
		}
		if p.Why != "" {
			row.Why = i18n.T(ctx, "routers.why."+p.Why)
		}
		switch p.Action {
		case routers.Update:
			row.Kind = "running"
		case routers.Remove:
			row.Kind = "broken"
		case routers.Unmanaged:
			row.Kind = "look"
		case routers.Forget:
			row.Kind = "off"
		default:
			row.Kind = "ok"
		}
		out = append(out, row)
	}
	return out
}

func (h *handler) rtPreviewOf(ctx context.Context, r routers.Router, s routers.Sync) rtPreviewView {
	loc := i18n.From(ctx)
	v := rtPreviewView{RouterID: r.ID, SyncID: s.ID, Running: s.State == "running", Failed: s.State == "failed" || s.State == "cancelled", Error: s.Error}
	if v.Running || v.Failed {
		return v
	}
	v.Read = i18n.T(ctx, "routers.preview.read", i18n.Args{"time": loc.Time(s.ReadAt) + s.ReadAt.In(loc.TZ).Format(":05")})
	var unmanaged int
	for _, p := range s.Plan {
		if p.Action == routers.Unmanaged {
			unmanaged++
		}
	}
	var parts []string
	if n := s.Added + s.Updated; n > 0 {
		parts = append(parts, loc.N("routers.preview.updates", int64(n)))
	}
	if s.Removed > 0 {
		parts = append(parts, loc.N("routers.preview.removals", int64(s.Removed)))
	}
	if s.Recorded > 0 {
		parts = append(parts, loc.N("routers.preview.recorded", int64(s.Recorded)))
	}
	parts = append(parts, loc.N("routers.preview.unchanged", int64(s.Unchanged)))
	if unmanaged > 0 {
		parts = append(parts, loc.N("routers.preview.unmanaged", int64(unmanaged)))
	}
	v.Counts = strings.Join(parts, " · ")
	v.Rows = planRows(ctx, s.Plan)
	v.Script = s.Script
	v.HasPushes = s.Added+s.Updated+s.Removed > 0
	v.ScriptHref = routerHref(r.ID) + "/syncs/" + i64(s.ID) + "/script.rsc"
	return v
}

func (h *handler) historyRow(ctx context.Context, r routers.Router, s routers.Sync) historyRow {
	loc := i18n.From(ctx)
	row := historyRow{ID: s.ID, Time: loc.Time(s.StartedAt), Kind: i18n.T(ctx, "routers.kind."+s.Kind),
		PlanHref: routerHref(r.ID) + "/syncs/" + i64(s.ID)}
	if s.JobID != 0 {
		row.JobHref = "/jobs/" + i64(s.JobID)
	}
	switch s.State {
	case "done":
		row.Marker, row.Word = "ok", i18n.T(ctx, "routers.result.done")
		var parts []string
		if s.Kind == "preview" {
			parts = append(parts, i18n.T(ctx, "routers.result.previewed"))
		} else {
			parts = append(parts, syncSummary(ctx, s))
		}
		if !s.FinishedAt.IsZero() {
			parts = append(parts, loc.Duration(s.FinishedAt.Sub(s.StartedAt).Round(time.Second)))
		}
		if s.Trigger != "" {
			parts = append(parts, i18n.T(ctx, "routers.trigger."+s.Trigger))
		}
		row.Detail = strings.Join(parts, " · ")
	case "failed":
		row.Marker, row.Word = "broken", i18n.T(ctx, "routers.result.failed", i18n.Args{"step": s.Step})
		row.Detail = s.Error
	case "cancelled":
		row.Marker, row.Word = "off", i18n.T(ctx, "routers.result.cancelled")
	default:
		row.Marker, row.Word = "running", i18n.T(ctx, "routers.result.running")
	}
	return row
}

func (h *handler) routerActivity(ctx context.Context, r routers.Router) ([]eventLine, error) {
	loc := i18n.From(ctx)
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: routers.Subject(r.ID), Limit: 10})
	if err != nil {
		return nil, err
	}
	var out []eventLine
	for _, e := range list {
		args := i18n.Args{"subject": r.Name, "actor": e.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		key := "event." + e.Type
		if e.Type == "routing.router_updated" {
			ch, _ := e.Payload["changes"].(string)
			first, _, _ := strings.Cut(ch, ", ")
			key = "routers.event." + first
		}
		text := e.Type
		if loc.Has(key) {
			text = loc.T(key, args)
		}
		out = append(out, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return out, nil
}

func (h *handler) routerSyncNow(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	if _, err := h.Routers.SyncNow(c.Request().Context(), r.ID, events.ActorAdmin); errors.Is(err, routers.ErrNotActive) {
		return echo.NewHTTPError(http.StatusConflict, "the router is not active")
	} else if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID)+"?synced=1")
}

func (h *handler) routerPreviewStart(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	if _, err := h.Routers.Preview(c.Request().Context(), r.ID, events.ActorAdmin); err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID)+"#preview")
}

// routerPreviewArea is the preview area, polling itself while it runs.
func (h *handler) routerPreviewArea(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	if !htmx(c) {
		return web.Redirect(c, routerHref(r.ID)+"#preview")
	}
	ctx := c.Request().Context()
	p, ok, err := h.Routers.LatestPreview(ctx, r.ID)
	if err != nil {
		return err
	}
	if !ok {
		return c.NoContent(http.StatusOK)
	}
	return web.Render(c, http.StatusOK, previewArea(h.rtPreviewOf(ctx, r, p)))
}

func (h *handler) loadSync(c *echo.Context, r routers.Router) (routers.Sync, error) {
	id, err := strconv.ParseInt(c.Param("sync"), 10, 64)
	if err != nil {
		return routers.Sync{}, echo.ErrNotFound
	}
	s, err := h.Routers.SyncRecord(c.Request().Context(), r.ID, id)
	if errors.Is(err, routers.ErrNotFound) {
		return s, echo.ErrNotFound
	}
	return s, err
}

type syncPageView struct {
	R    routers.Router
	S    routers.Sync
	Row  historyRow
	Rows []planRow
	Read string
}

func (h *handler) routerSyncPage(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	s, err := h.loadSync(c, r)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := syncPageView{R: r, S: s, Row: h.historyRow(ctx, r, s), Rows: planRows(ctx, s.Plan)}
	if !s.ReadAt.IsZero() {
		v.Read = i18n.From(ctx).Time(s.ReadAt)
	}
	title := i18n.T(ctx, "routers.sync.title", i18n.Args{"kind": i18n.T(ctx, "routers.kind."+s.Kind), "id": s.ID})
	return web.Render(c, http.StatusOK, routerSyncPage(h.shell(c, title, routersPath), v, title))
}

func (h *handler) routerScript(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	s, err := h.loadSync(c, r)
	if err != nil {
		return err
	}
	if s.Script == "" {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="proxier-sync-%s-preview.rsc"`, strings.ToLower(safeFile(r.Name))))
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", []byte(s.Script))
}

func safeFile(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// ------------------------------------------------------------------ edit connection, routing list

type editView struct {
	R    routers.Router
	F    routerForm
	Errs map[string]string
}

func (h *handler) routerEditPage(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	return h.renderEdit(c, http.StatusOK, editView{R: r, F: routerForm{Name: r.Name, ListID: r.ListID, Conn: r.Conn, FirstHop: hopOf(r.Conn)}})
}

func (h *handler) renderEdit(c *echo.Context, status int, v editView) error {
	ctx := c.Request().Context()
	title := i18n.T(ctx, "routers.edit.title", i18n.Args{"name": v.R.Name})
	return web.Render(c, status, routerEditPage(h.shell(c, title, routersPath), v, title))
}

func (h *handler) routerEdit(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	f := routerForm{Name: c.FormValue("name"), ListID: r.ListID, Conn: formConn(c), FirstHop: c.FormValue("first_hop")}
	_, err = h.Routers.Edit(ctx, r.ID, f.Name, f.Conn, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		return h.renderEdit(c, http.StatusUnprocessableEntity, editView{R: r, F: f, Errs: errText(ctx, fe)})
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID)+"?saved=1")
}

type listPickView struct {
	R     routers.Router
	Lists []lists.List
	Err   string
}

func (h *handler) routerListPage(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	return h.renderListPick(c, http.StatusOK, listPickView{R: r})
}

func (h *handler) renderListPick(c *echo.Context, status int, v listPickView) error {
	ctx := c.Request().Context()
	all, err := h.Lists.All(ctx)
	if err != nil {
		return err
	}
	v.Lists = all
	title := i18n.T(ctx, "routers.list.title", i18n.Args{"name": v.R.Name})
	return web.Render(c, status, routerListPage(h.shell(c, title, routersPath), v, title))
}

func (h *handler) routerSetList(c *echo.Context) error {
	r, err := h.loadRouter(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	err = h.Routers.SetList(ctx, r.ID, formID(c.FormValue("list")), events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		return h.renderListPick(c, http.StatusUnprocessableEntity, listPickView{R: r, Err: i18n.T(ctx, fe["list"])})
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, routerHref(r.ID)+"?saved=1")
}

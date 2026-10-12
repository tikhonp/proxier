package pages_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/ui"
)

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

func insertResult(t *testing.T, h *serverstest.Harness, id int64, r store.CheckResult) {
	t.Helper()
	r.ServerID = id
	if r.At.IsZero() {
		r.At = db.Now()
	}
	if err := h.App.DB.Write(bg, func(tx *sqlx.Tx) error { return store.InsertCheckResult(bg, tx, r) }); err != nil {
		t.Fatal(err)
	}
}

// setHealth puts a server into a health state with a stored reason.
func setHealth(t *testing.T, h *serverstest.Harness, id int64, state, reasonKey string, args map[string]any) {
	t.Helper()
	reason := js(map[string]any{"key": reasonKey, "args": args})
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET health = ?, health_since = ?, health_reason = ? WHERE id = ?`,
		state, db.At(time.Now().Add(-2*time.Hour)), reason, id); err != nil {
		t.Fatal(err)
	}
}

func TestHealthTab(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)
	now := db.Now()
	insertResult(t, h, id, store.CheckResult{Kind: "self", Vantage: "home", OK: true, Class: "ok", At: now,
		Detail: js(map[string]any{"checks": []map[string]any{{"kind": "compose-running", "pass": true, "message": "2 containers running"}, {"kind": "disk-free", "pass": true, "message": "60% of the disk is free"}}})})
	insertResult(t, h, id, store.CheckResult{Kind: "proxy", EndpointKey: "main", Vantage: "home", OK: false, Class: "stalled", At: now,
		Detail: js(map[string]any{"error": "no data for 5s after 16 KB received"})})
	insertResult(t, h, id, store.CheckResult{Kind: "proxy", EndpointKey: "main", Vantage: "home", OK: true, At: db.At(now.Add(-time.Hour)),
		Detail: js(map[string]any{"first_byte_ms": 210, "kbps": 4200, "connect_ms": 40})})
	for node, ok := range map[string]bool{"ru1.node.check-host.net": false, "ru2.node.check-host.net": false, "de1.node.check-host.net": true, "fi1.node.check-host.net": true} {
		region, country := "abroad", "de"
		if strings.HasPrefix(node, "ru") {
			region, country = "ru", "ru"
		}
		class := ""
		if !ok {
			class = "timeout"
		}
		insertResult(t, h, id, store.CheckResult{Kind: "external", Vantage: node, OK: ok, Class: class, At: now,
			Detail: js(map[string]any{"region": region, "country": country, "ms": 31, "error": map[bool]string{false: "Connection timed out"}[ok]})})
	}
	setHealth(t, h, id, "blocked", "health.reason.blocked_abroad", map[string]any{"failure": "stalled: no data for 5s after 16 KB received", "ru_ok": 0, "ru": 2, "countries": []string{"de", "fi"}})
	// the change that led here
	if err := h.App.DB.Write(bg, func(tx *sqlx.Tx) error {
		_, err := h.App.Events.Record(bg, tx, eventOf(id, "healthy", "blocked"))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	body := page(t, h, base+"/health")
	mustContain(t, body,
		"Blocked", "Reachable from Germany and Finland, but the proxy fails from home", "0/2 Russian nodes connect", // verdict and its reason
		"Run checks now", "Pause checks", "Health timeline",
		"Reference check", "Self-check over SSH", "compose-running", "2 containers running", // the matrix: home, self-check and each of its checks
		"Proxy test from home", "stalled", "no data for 5s after 16 KB received",
		"Nodes in Russia", "Nodes abroad", "timeout", "connected",
		`class="strip"`, `class="timeline"`, // history and timeline
		"healthy", "→", // the change list
	)
	mustNotContain(t, body, "style=")
	// the timeline ranges
	for _, r := range []string{"7d", "30d"} {
		b := page(t, h, base+"/health?range="+r)
		mustContain(t, b, "Health timeline", `aria-pressed="true"`)
	}
	// a paused server shows Resume and the end of the pause
	setHealth(t, h, id, "paused", "health.reason.paused_forever", nil)
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET checks_paused_until = ? WHERE id = ?`, store.PausedForever, id); err != nil {
		t.Fatal(err)
	}
	b := page(t, h, base+"/health")
	mustContain(t, b, "Resume checks", "paused until you resume")
	mustNotContain(t, b, "Run checks now</button>\n")

	// the pause dialog (htmx) and the Run checks now redirect
	rec := h.Login.Get(base+"/pause", http.Header{"Hx-Request": {"true"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<dialog id="pause-dialog"`) || !strings.Contains(rec.Body.String(), "until I resume") {
		t.Fatalf("pause dialog %d\n%s", rec.Code, rec.Body)
	}
	// an external result was stored a moment ago, so the external check is over its budget
	rec = h.Login.Post(base+"/checks/run", url.Values{})
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "/health?msg=over_budget&run=") {
		t.Fatalf("run now: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	mustContain(t, page(t, h, rec.Header().Get("Location")), "the external check is over its limit", `hx-trigger="every 2s"`)
	if rec := h.Login.Post(base+"/pause", url.Values{"d": {"6h"}}); rec.Code != 303 {
		t.Fatalf("pause: %d\n%s", rec.Code, rec.Body)
	}
	if h.Server(id).Health != "paused" {
		t.Errorf("health %s", h.Server(id).Health)
	}
	if rec := h.Login.Post(base+"/pause", url.Values{"d": {"nonsense"}}); rec.Code != 422 {
		t.Errorf("a bad duration: %d", rec.Code)
	}
	if rec := h.Login.Post(base+"/resume", url.Values{}); rec.Code != 303 {
		t.Fatalf("resume: %d", rec.Code)
	}
	if h.Server(id).Health != "unknown" {
		t.Errorf("health %s after resume", h.Server(id).Health)
	}
	// a server that is not active has no Health tab
	if rec := h.Login.Get("/servers/9999/health"); rec.Code != 404 {
		t.Errorf("unknown server: %d", rec.Code)
	}
}

// verdictOf is the verdict section of a Health tab.
func verdictOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<section class="verdict`)
	if i < 0 {
		t.Fatalf("no verdict:\n%s", excerpt(body))
	}
	return body[i : i+strings.Index(body[i:], "</section>")]
}

// The Health tab draws nothing that has nothing in it: a healthy verdict has
// no reason, so no body (which stood as an empty padded band under the
// verdict), and no group of the matrix is a heading without rows.
func TestHealthTabHasNoEmptyBlocks(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)

	setHealth(t, h, id, "healthy", "", nil)
	body := page(t, h, base+"/health")
	mustContain(t, verdictOf(t, body), "Healthy", "Run checks now", "Pause checks")
	mustNotContain(t, body, `class="verdict-body"`)
	// and the same while a run of checks is fresh
	run := page(t, h, base+"/health?msg=queued&run="+strconv.FormatInt(time.Now().Unix(), 10))
	mustContain(t, run, "Checks are queued", `hx-trigger="every 2s"`)
	mustNotContain(t, run, `class="verdict-body"`)
	// without a body the head draws no divider of its own over the section's
	css, err := fs.ReadFile(ui.StaticFS, "css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `.verdict-head:last-child { border-bottom: 0; }`) {
		t.Error("app.css lacks the verdict head's rule without a body")
	}

	// before the first external check one heading holds the row that says so
	mustContain(t, body, "Nodes in Russia and abroad (check-host.net)", ">check-host.net", "no result yet")
	mustNotContain(t, body, ">Nodes in Russia (check-host.net)<", ">Nodes abroad (check-host.net)<")
	// a round with nodes abroad only: the Russian group says it had none
	insertResult(t, h, id, store.CheckResult{Kind: "external", Vantage: "de1.node.check-host.net", OK: true, Detail: js(map[string]any{"region": "abroad", "country": "de", "ms": 31})})
	body = page(t, h, base+"/health")
	mustContain(t, body, ">Nodes in Russia (check-host.net)<", "no node in the latest check", ">Nodes abroad (check-host.net)<", "connected")
	mustNotContain(t, body, "Nodes in Russia and abroad")

	// a reason gives the verdict a body
	setHealth(t, h, id, "degraded", "health.reason.slow_first_byte", map[string]any{"seconds": "2.4"})
	mustContain(t, verdictOf(t, page(t, h, base+"/health")), `class="verdict-body"`, "Slow: 2.4 s to first byte")

	// paused: the note says the pause once, with what it means
	setHealth(t, h, id, "paused", "health.reason.paused_forever", nil)
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET checks_paused_until = ? WHERE id = ?`, store.PausedForever, id); err != nil {
		t.Fatal(err)
	}
	v := verdictOf(t, page(t, h, base+"/health"))
	mustContain(t, v, `class="verdict-body"`, "Checks are paused until you resume them; no notifications are sent.")
	mustNotContain(t, v, "Checks paused until you resume them")
}

func TestStatsTab(t *testing.T) {
	h, id := provisioned(t)
	base := "/servers/" + sid(id)
	mustContain(t, page(t, h, base+"/stats"), "No samples yet")

	start := time.Now().UTC().Add(-3 * time.Hour)
	n := 0
	for i := 0; i <= 30; i++ {
		if i > 10 && i < 17 { // 30 minutes unreachable: six samples missing
			continue
		}
		r := remote.StatsReading{
			Load1: 0.4, CPUBusy: uint64(1000 + 100*n), CPUTotal: uint64(10000 + 1000*n), MemUsed: 1 << 29, MemTotal: 2 << 30,
			DiskUsed: 5 << 30, DiskTotal: 20 << 30, Iface: "eth0", RXBytes: uint64(1e6 + 5e5*float64(n)), TXBytes: uint64(2e6 + 1e5*float64(n)),
			Uptime: float64(100000 + 300*n), Containers: []remote.Container{{Name: "stack-nginx-1", State: "running", Image: "nginx:stable-alpine"}, {Name: "stack-xray-1", State: "restarting", Restarts: 3, Image: "xray:latest"}},
		}
		if err := h.App.DB.Write(bg, func(tx *sqlx.Tx) error {
			return h.Mod.Stats.Store(bg, tx, id, start.Add(time.Duration(i)*5*time.Minute), r)
		}); err != nil {
			t.Fatal(err)
		}
		n++
	}
	body := page(t, h, base+"/stats")
	mustContain(t, body, "CPU", "Load (1 min)", "Memory", "Disk", "Traffic", "Traffic today", "Traffic this month", "Uptime",
		"Containers", "stack-xray-1", "restarting", "xray:latest", "Breaks in a line are missing samples",
		`id="chart-cpu"`, `id="chart-traffic"`, `class="chart"`, "25 %" /* memory is 512 MiB of 2 GiB */)
	mustNotContain(t, body, "style=")
	// the gap breaks the line: the CPU path has more than one subpath
	i := strings.Index(body, `id="chart-cpu"`)
	seg := body[i:]
	d := seg[strings.Index(seg, `class="chart-line`):]
	d = d[strings.Index(d, ` d="`)+4:]
	d = d[:strings.Index(d, `"`)]
	if strings.Count(d, "M") < 2 {
		t.Errorf("the CPU line has %d subpaths; the 30 minute gap should break it: %s", strings.Count(d, "M"), d)
	}
	for _, r := range []string{"7d", "30d"} {
		mustContain(t, page(t, h, base+"/stats?range="+r), "Containers", `id="chart-cpu"`)
	}
}

func TestServersDashboard(t *testing.T) {
	h, id := provisioned(t)
	// healthy fleet, nothing to look at
	setHealth(t, h, id, "healthy", "", nil)
	body := page(t, h, "/")
	mustContain(t, body, "Servers", "Healthy")
	mustNotContain(t, body, "Home is offline")
	// states no server is in are left out, not shown as 0
	for _, st := range []string{"Provisioning", "Failed", "Degraded", "Blocked", "Down", "Unknown", "Paused"} {
		mustNotContain(t, body, ">"+st+"<")
	}

	// a blocked server with its reason, and home offline
	setHealth(t, h, id, "blocked", "health.reason.blocked_unconfirmed", map[string]any{"failure": "stalled"})
	if _, err := h.App.DB.W.Exec(`INSERT INTO servers_home (id, state, since) VALUES (1, 'offline', ?)`, db.At(time.Now().Add(-12*time.Minute))); err != nil {
		t.Fatal(err)
	}
	body = page(t, h, "/")
	mustContain(t, body, "Home is offline", "Server verdicts are on hold", "nl-1", "Blocked", "the proxy fails from home", "since")

	// foreign unreachable is another banner
	if _, err := h.App.DB.W.Exec(`UPDATE servers_home SET state = 'foreign-unreachable'`); err != nil {
		t.Fatal(err)
	}
	mustContain(t, page(t, h, "/"), "Foreign internet is unreachable from home")

	// a newer default version, and the top lists
	h.PublishVersion(func(files map[string][]byte) {
		files["site/index.html"] = append(files["site/index.html"], []byte("<!-- v2 -->")...)
	}, true)
	if err := h.App.DB.Write(bg, func(tx *sqlx.Tx) error {
		return h.Mod.Stats.Store(bg, tx, id, time.Now(), remote.StatsReading{Load1: 1, MemUsed: 1, MemTotal: 2, DiskUsed: 9, DiskTotal: 10, Uptime: 10, CPUTotal: 10, CPUBusy: 1})
	}); err != nil {
		t.Fatal(err)
	}
	body = page(t, h, "/")
	mustContain(t, body, "Update available", "v2 is available", "Most disk used", "90 %")
}

func eventOf(id int64, from, to string) events.Event {
	return events.Event{Type: "server.health_changed", Subject: store.ServerSubject(id), Actor: "system",
		Payload: map[string]any{"from": from, "to": to, "reason": "x", "reason_key": "health.reason.blocked_unconfirmed", "reason_args": map[string]any{"failure": "stalled"}}}
}

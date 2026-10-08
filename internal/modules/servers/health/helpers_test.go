package health_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/checkhost/checkhosttest"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

var bg = context.Background()

// clock is the fake time of the health service and of the job system.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fixture is an active server on the full harness, with a fake clock, no
// schedules (a test queues the jobs it wants), a local reference site and a
// fake check-host.net. Results are inserted directly, so a test says exactly
// what the checks found.
type fixture struct {
	*serverstest.Harness
	ID    int64
	Clock *clock
	Svc   *health.Service
	CH    *checkhosttest.Fake
	// Ref is the reference site: both lists point at it. Fail it with SetRef.
	ref   *httptest.Server
	refMu sync.Mutex
	refOK map[string]bool // path → answers
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	if h.Server(id).State != "active" {
		t.Fatalf("not active: %s", h.Server(id).FailedError)
	}
	f := &fixture{Harness: h, ID: id, refOK: map[string]bool{"/dom": true, "/for": true}}
	f.Clock = &clock{t: time.Now().UTC().Truncate(time.Millisecond)}
	f.Svc = h.Mod.Health
	f.Svc.Now = f.Clock.Now
	f.Svc.EvalDelay = 2 * time.Minute
	f.Svc.ProxyDelay = 0 // a round's proxy test follows its self-check at once; a test that cares sets it

	f.CH = checkhosttest.New(t)
	f.Svc.CheckHost = f.CH.Client

	f.ref = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.refMu.Lock()
		ok := f.refOK[r.URL.Path]
		f.refMu.Unlock()
		if !ok {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.ref.Close)
	if err := h.App.Settings.Set(bg, "admin", "servers", map[string]string{
		"servers.reference_domestic": f.ref.URL + "/dom",
		"servers.reference_foreign":  f.ref.URL + "/for",
	}); err != nil {
		t.Fatal(err)
	}

	// The job system gets the fake clock after its workers are stopped, and the
	// schedules are switched off: a test queues what it wants to run.
	h.StopJobs()
	h.App.Jobs.Now = f.Clock.Now
	for _, s := range f.Svc.Schedules() {
		if err := h.App.Jobs.SetScheduleEnabled(bg, s.Name, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	h.StartJobs()
	return f
}

// SetRef makes a reference path ("/dom", "/for") answer or fail.
func (f *fixture) SetRef(path string, ok bool) {
	f.refMu.Lock()
	f.refOK[path] = ok
	f.refMu.Unlock()
}

func (f *fixture) at() db.Time { return db.At(f.Clock.Now()) }

func (f *fixture) insert(r store.CheckResult) {
	f.T.Helper()
	r.ServerID, r.At = f.ID, f.at()
	if err := f.App.DB.Write(bg, func(tx *sqlx.Tx) error { return store.InsertCheckResult(bg, tx, r) }); err != nil {
		f.T.Fatal(err)
	}
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

// Proxy stores a proxy test of an endpoint.
func (f *fixture) Proxy(key string, ok bool, class, errText string, firstByteMS, kbps int) {
	f.T.Helper()
	f.insert(store.CheckResult{Kind: "proxy", EndpointKey: key, Vantage: "home", OK: ok, Class: class,
		Detail: js(map[string]any{"error": errText, "first_byte_ms": firstByteMS, "kbps": kbps, "connect_ms": 40})})
}

// ProxyOK stores a quick, fast proxy test.
func (f *fixture) ProxyOK(key string) { f.T.Helper(); f.Proxy(key, true, "", "", 200, 8000) }

// ProxyFail stores a failed proxy test.
func (f *fixture) ProxyFail(key, class, errText string) {
	f.T.Helper()
	f.Proxy(key, false, class, errText, 0, 0)
}

// Self stores a self-check result.
func (f *fixture) Self(class string, failing ...health.Failure) {
	f.T.Helper()
	checks := []map[string]any{{"kind": "disk-free", "pass": true, "message": "60% free"}}
	for _, x := range failing {
		checks = append(checks, map[string]any{"kind": x.Kind, "pass": false, "message": x.Message})
	}
	f.insert(store.CheckResult{Kind: "self", Vantage: "home", OK: class == health.SelfOK, Class: class,
		Detail: js(map[string]any{"checks": checks, "error": map[string]string{health.SelfUnreachable: "i/o timeout"}[class]})})
}

// External stores a check-host result: which Russian nodes and which nodes
// abroad connected.
func (f *fixture) External(ru, abroad []bool) {
	f.T.Helper()
	at := f.at()
	row := func(node, region, country string, ok bool) {
		class := ""
		if !ok {
			class = "timeout"
		}
		r := store.CheckResult{ServerID: f.ID, Kind: "external", Vantage: node, At: at, OK: ok, Class: class,
			Detail: js(map[string]any{"region": region, "country": country, "ms": 30})}
		if err := f.App.DB.Write(bg, func(tx *sqlx.Tx) error { return store.InsertCheckResult(bg, tx, r) }); err != nil {
			f.T.Fatal(err)
		}
	}
	for i, ok := range ru {
		row("ru"+strconv.Itoa(i+1)+".node.check-host.net", "ru", "ru", ok)
	}
	for i, ok := range abroad {
		row([]string{"de1", "nl1", "fi1"}[i]+".node.check-host.net", "abroad", []string{"de", "nl", "fi"}[i], ok)
	}
}

// Eval gives the verdict from what is stored.
func (f *fixture) Eval() {
	f.T.Helper()
	if err := f.Svc.Evaluate(bg, f.ID, "test"); err != nil {
		f.T.Fatal(err)
	}
}

// Tick moves the clock.
func (f *fixture) Tick(d time.Duration) { f.Clock.Add(d) }

// Health reads the server's health columns.
func (f *fixture) Health() store.Health {
	f.T.Helper()
	h, err := store.GetHealth(bg, f.App.DB.R, f.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	return h
}

// Changes lists the server.health_changed events, oldest first.
func (f *fixture) Changes() []events.Event {
	f.T.Helper()
	l := f.Events("server.health_changed")
	for i, j := 0, len(l)-1; i < j; i, j = i+1, j-1 {
		l[i], l[j] = l[j], l[i]
	}
	return l
}

// Notifying counts the events a notification would be sent for.
func (f *fixture) Notifying(typ string) int {
	f.T.Helper()
	t, _ := f.App.Events.Lookup(typ)
	n := 0
	for _, e := range f.Events(typ) {
		if t.Notify && (t.NotifyIf == nil || t.NotifyIf(e.Payload)) {
			n++
		}
	}
	return n
}

// ReasonText is the English reason of the latest change.
func (f *fixture) ReasonText() string {
	c := f.Changes()
	if len(c) == 0 {
		return ""
	}
	s, _ := c[len(c)-1].Payload["reason"].(string)
	return s
}

// Healthy takes the server to healthy with one good round.
func (f *fixture) Healthy() {
	f.T.Helper()
	f.Self(health.SelfOK)
	f.ProxyOK("main")
	f.Eval()
	if got := f.Health().Health; got != health.Healthy {
		f.T.Fatalf("health %s", got)
	}
}

// AddEndpoint gives the server a second endpoint.
func (f *fixture) AddEndpoint(key string) {
	f.T.Helper()
	rows, err := store.Endpoints(bg, f.App.DB.R, f.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	eps, err := sealed.OpenEndpoints(f.App.Vault, rows)
	if err != nil {
		f.T.Fatal(err)
	}
	e := eps[0]
	e.Key = key
	e.Params = map[string]string{"path": "/other", "sni": e.Host}
	e.DisplayName = key
	eps = append(eps, endpoint.Endpoint(e))
	sealedRows := sealed.SealEndpoints(f.App.Vault, f.ID, eps)
	for i := range sealedRows {
		sealedRows[i].Position = i
	}
	if err := f.App.DB.Write(bg, func(tx *sqlx.Tx) error {
		return store.ReplaceEndpoints(bg, tx, f.ID, sealedRows, f.at())
	}); err != nil {
		f.T.Fatal(err)
	}
}

// runJob queues a job of a type and waits until it has run.
func (f *fixture) runJob(typ string) {
	f.T.Helper()
	if _, err := f.App.Jobs.EnqueueNow(bg, jobsRequest(typ)); err != nil {
		f.T.Fatal(err)
	}
	f.Drain()
}

func homeOf(f *fixture) (string, db.Time, error) { return store.GetHome(bg, f.App.DB.R) }

func newCatalogLocalizer(t *testing.T) *i18n.Localizer {
	t.Helper()
	cat := i18n.NewCatalog()
	if err := cat.Add("servers", servers.New().Messages()); err != nil {
		t.Fatal(err)
	}
	return cat.Localizer(i18n.EN, time.UTC)
}

func jobsRequest(typ string) jobs.Request {
	return jobs.Request{Type: typ, CreatedBy: "test", Payload: map[string]any{}}
}

func sid(id int64) string { return strconv.FormatInt(id, 10) }

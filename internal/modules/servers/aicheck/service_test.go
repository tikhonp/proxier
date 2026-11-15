package aicheck_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/aicheck"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

type fixture struct {
	t          *testing.T
	site       *sitetest.Site
	m          *servers.Module
	sub        *subscriptions.Module
	id         int64
	now        time.Time
	mu         sync.Mutex
	ips        map[string]string
	traceFail  bool
	traceCalls int
	apiCalls   int
	response   func(ip string) (int, string)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	m := servers.New()
	sub := subscriptions.New(subscriptions.Ports{Catalog: m.EndpointCatalog()})
	f := &fixture{t: t, m: m, sub: sub, now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), ips: map[string]string{"main": "8.8.8.8", "extra": "1.1.1.1"}}
	f.site = sitetest.New(t, sitetest.Options{Modules: []module.Module{m, sub}})
	m.AI.Now = func() time.Time { return f.now }
	sub.Now = m.AI.Now
	f.setting(conf.AIEnabled, "true")
	f.write(func(tx *sqlx.Tx) error {
		var err error
		f.id, err = store.InsertServer(t.Context(), tx, store.NewServer{Country: "NL", Number: 1, Name: "nl-1", IP: "8.8.8.8", SSHPort: 22, ManagementHostname: "nl-1.example.test", ProxyHostname: "nl-1.example.test", TemplateID: 1, TemplateVersion: 1, Params: "{}"}, db.At(f.now), nil)
		if err != nil {
			return err
		}
		if err := store.MarkActive(t.Context(), tx, f.id, 1, db.At(f.now)); err != nil {
			return err
		}
		return store.ReplaceEndpoints(t.Context(), tx, f.id, sealed.SealEndpoints(f.site.App.Vault, f.id, []endpoint.Endpoint{
			{Key: "main", Type: endpoint.VlessXHTTPTLS, Host: "nl-1.example.test", Port: 443, Credential: "main-credential", Params: map[string]string{"path": "/secret-main"}, DisplayName: "Netherlands 1 · main"},
			{Key: "extra", Type: endpoint.VlessXHTTPTLS, Host: "extra.example.test", Port: 443, Credential: "extra-credential", Params: map[string]string{"path": "/secret-extra"}, DisplayName: "Netherlands 1 · extra"},
		}), db.At(f.now))
	})
	addresses := map[string]string{}
	for _, key := range []string{"main", "extra"} {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.traceCalls++
			ip, fail := f.ips[key], f.traceFail
			f.mu.Unlock()
			if fail {
				w.WriteHeader(503)
				return
			}
			if r.Host == "api.test" {
				w.WriteHeader(401)
				return
			}
			_, _ = io.WriteString(w, "ip="+ip+"\n")
		}))
		t.Cleanup(h.Close)
		addresses[key] = strings.TrimPrefix(h.URL, "http://")
	}
	m.AI.TraceURLs = map[string]string{"claude": "http://claude.test/cdn-cgi/trace", "chatgpt": "http://chatgpt.test/cdn-cgi/trace"}
	m.AI.APIURL = "http://api.test/"
	m.AI.Dial = func(ctx context.Context, e endpoint.Endpoint) (proxy.DialFunc, io.Closer, error) {
		if e.Credential != e.Key+"-credential" {
			t.Errorf("wrong endpoint credential for %s", e.Key)
		}
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !strings.HasSuffix(addr, ".test:80") {
				return nil, fmt.Errorf("unexpected destination %s", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, addresses[e.Key])
		}, closeFunc(func() error { return nil }), nil
	}
	m.AI.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		f.apiCalls++
		if r.URL.Host != "ip.net.coffee" || !strings.HasPrefix(r.URL.Path, "/api/iprisk/") {
			t.Fatalf("unexpected home request %s", r.URL)
		}
		ip := strings.TrimPrefix(r.URL.Path, "/api/iprisk/")
		status, body := 200, `{"ip":"`+ip+`","trust_score":85,"countryCode":"nl","is_vpn":null,"is_datacenter":true}`
		if f.response != nil {
			status, body = f.response(ip)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"7200"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	return f
}

func (f *fixture) write(fn func(*sqlx.Tx) error) {
	f.t.Helper()
	if err := f.site.App.DB.Write(f.t.Context(), fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) setting(key, value string) {
	f.t.Helper()
	if err := f.site.App.Settings.Set(f.t.Context(), "member:1", "servers", map[string]string{key: value}); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) check() {
	f.t.Helper()
	if err := f.m.AI.Check(f.t.Context(), f.id, "member:1"); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) scores() map[string]map[string]float64 {
	f.t.Helper()
	r, err := f.m.AI.Scores(f.t.Context(), f.id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f *fixture) changeIP(key, ip string) { f.mu.Lock(); defer f.mu.Unlock(); f.ips[key] = ip }

func TestChecksCacheAndServicePolicy(t *testing.T) {
	f := newFixture(t)
	f.changeIP("extra", "8.8.8.8")
	f.response = func(ip string) (int, string) {
		return 200, `{"ip":"` + ip + `","trust_score":85,"countryCode":"ru","is_vpn":null,"is_proxy":false}`
	}
	f.check()
	if f.apiCalls != 1 {
		t.Fatalf("shared IP used %d lookups", f.apiCalls)
	}
	for key, scores := range f.scores() {
		if scores["ip"] != 85 || scores["claude"] != 0 || scores["chatgpt"] != 0 {
			t.Fatalf("%s: %v", key, scores)
		}
	}
	rows, err := f.m.AI.Read(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("got %d service rows", len(rows))
	}
	if rows[2].Attempt.HTTPStatus != 401 || !rows[2].Attempt.Reachable || rows[2].Success != nil {
		t.Fatal("API response claimed an exit IP or score")
	}
	f.now = f.now.Add(time.Hour)
	f.check()
	if f.apiCalls != 1 {
		t.Fatal("manual check bypassed cache")
	}
	f.now = f.now.Add(24 * time.Hour)
	f.check()
	if f.apiCalls != 2 {
		t.Fatal("expired cache reused")
	}
	ev, err := events.List(t.Context(), f.site.App.DB.R, events.Filter{Type: "server.ai_check_completed", Limit: 100})
	if err != nil || len(ev) != 18 {
		t.Fatalf("events %d %v", len(ev), err)
	}
	for _, e := range ev {
		if e.Actor != "member:1" {
			t.Fatal(e.Actor)
		}
	}
}

func TestCIDRCachedReputationKeepsObservedExitAndExactIPCache(t *testing.T) {
	f := newFixture(t)
	f.changeIP("main", "87.121.82.109")
	f.changeIP("extra", "87.121.82.110")
	f.response = func(string) (int, string) {
		return 200, `{"ip":"87.121.82.209","cidr":"87.121.82.0/24","trust_score":86,"countryCode":"fi"}`
	}
	f.check()
	if f.apiCalls != 2 {
		t.Fatalf("two observed IPs used %d lookups", f.apiCalls)
	}
	rows, err := f.m.AI.Read(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Service == aicheck.OpenAIAPI {
			continue
		}
		want := f.ips[row.EndpointKey]
		if !row.Fresh || row.CurrentIP != want || row.Success == nil || row.Success.IP != want {
			t.Fatalf("reputation replaced observed exit: %+v", row)
		}
		modes := []string{row.Service}
		if row.Service == aicheck.Claude {
			modes = append(modes, aicheck.ReputationMode)
		}
		for _, mode := range modes {
			if score, ok := row.Score(mode); !ok || score != 86 {
				t.Fatalf("%s score %v, available %v", mode, score, ok)
			}
		}
	}
	f.check()
	if f.apiCalls != 2 {
		t.Fatal("normalized reputation did not reuse exact-IP cache")
	}
}

func TestFailedRefreshRetainsSuccessButChangedIPNeverResurrectsIt(t *testing.T) {
	f := newFixture(t)
	f.check()
	if f.apiCalls != 2 {
		t.Fatalf("distinct exits shared cache: %d", f.apiCalls)
	}
	f.now = f.now.Add(25 * time.Hour)
	f.response = func(string) (int, string) { return 200, `{"trust_score":null}` }
	f.check()
	if score, ok := f.scores()["main"]["claude"]; !ok || score != 85 {
		t.Fatal("failed refresh lost fresh success")
	}
	rows, _ := f.m.AI.Read(t.Context(), f.id)
	if rows[0].Attempt.Error == "" || rows[0].Success == nil || !rows[0].Fresh {
		t.Fatalf("attempt/success %+v", rows[0])
	}
	f.changeIP("main", "9.9.9.9")
	f.check()
	if _, ok := f.scores()["main"]["claude"]; ok {
		t.Fatal("old IP score survived new IP lookup failure")
	}
	f.changeIP("main", "8.8.8.8")
	f.check()
	if _, ok := f.scores()["main"]["claude"]; ok {
		t.Fatal("returning to the old IP restored its score without a successful lookup")
	}
	f.mu.Lock()
	f.traceFail = true
	f.mu.Unlock()
	f.check()
	if _, ok := f.scores()["main"]["claude"]; ok {
		t.Fatal("trace failure resurrected previous IP score")
	}
	f.now = f.now.Add(24 * time.Hour)
	if len(f.scores()) != 0 {
		t.Fatal("stale results served")
	}
}

func TestRateLimitPersistsAcrossChecks(t *testing.T) {
	f := newFixture(t)
	f.response = func(string) (int, string) { return 429, "" }
	f.check()
	if f.apiCalls != 1 {
		t.Fatal("fleet ignored rate limit")
	}
	f.now = f.now.Add(time.Hour)
	f.check()
	if f.apiCalls != 1 {
		t.Fatal("retry time ignored")
	}
	f.now = f.now.Add(2 * time.Hour)
	f.check()
	if f.apiCalls != 2 {
		t.Fatal("cooldown never expired")
	}
	if len(f.scores()) != 0 {
		t.Fatal("429 became a score")
	}
}

func TestNoDirectFallbackAndDisabledChecks(t *testing.T) {
	f := newFixture(t)
	f.m.AI.Dial = func(context.Context, endpoint.Endpoint) (proxy.DialFunc, io.Closer, error) {
		return nil, nil, fmt.Errorf("failed")
	}
	f.check()
	if f.apiCalls != 0 {
		t.Fatal("lookup after failed proxy")
	}
	rows, _ := f.m.AI.Read(t.Context(), f.id)
	for _, r := range rows {
		if r.Attempt.Error != "proxy dialer failed" || r.Attempt.Reachable {
			t.Fatal("proxy error lost")
		}
	}
	f.setting(conf.AIEnabled, "false")
	if err := f.m.AI.Check(t.Context(), f.id, "member:1"); err != aicheck.ErrDisabled {
		t.Fatal(err)
	}
	if _, err := f.m.AI.Queue(t.Context(), f.id, "member:1"); err != aicheck.ErrDisabled {
		t.Fatal(err)
	}
	if len(f.scores()) != 0 {
		t.Fatal("disabled integration serves scores")
	}
}

func TestConfigurationChangesAndRetirementInvalidateResults(t *testing.T) {
	for _, change := range []string{"ip", "endpoint", "address", "retiring", "retired"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			f.check()
			f.write(func(tx *sqlx.Tx) error {
				var err error
				switch change {
				case "ip":
					_, err = tx.Exec(`UPDATE servers_servers SET ip='9.9.9.9',ip_changed_at=? WHERE id=?`, db.At(f.now.Add(time.Minute)), f.id)
				case "endpoint":
					_, err = tx.Exec(`UPDATE servers_endpoints SET port=8443 WHERE server_id=?`, f.id)
				case "address":
					_, err = store.InsertAddress(t.Context(), tx, f.id, "9.9.9.9", "extra2.example.test", db.At(f.now))
				case "retiring":
					_, err = tx.Exec(`UPDATE servers_servers SET retire_job_id=123 WHERE id=?`, f.id)
				case "retired":
					_, err = tx.Exec(`UPDATE servers_servers SET state='retired',health=NULL,retired_at=? WHERE id=?`, db.At(f.now), f.id)
				}
				return err
			})
			if len(f.scores()) != 0 {
				t.Fatal("old scores survived configuration change")
			}
		})
	}
}

func TestLateResultFromOldConfigurationCannotBecomeCurrent(t *testing.T) {
	f := newFixture(t)
	changed := false
	f.response = func(ip string) (int, string) {
		if !changed {
			changed = true
			f.write(func(tx *sqlx.Tx) error {
				_, err := tx.Exec(`UPDATE servers_endpoints SET port=8443 WHERE server_id=?`, f.id)
				return err
			})
		}
		return 200, `{"ip":"` + ip + `","trust_score":85,"countryCode":"nl"}`
	}
	f.check()
	if len(f.scores()) != 0 {
		t.Fatal("late result became current")
	}
}

func TestSubscriptionFetchPreviewAndPermissions(t *testing.T) {
	f := newFixture(t)
	f.check()
	subID, err := f.sub.Subs.Create(t.Context(), 1, "Family", "", "", "member:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sub.Subs.AddServers(t.Context(), subID, access.All, []int64{f.id}, "member:1"); err != nil {
		t.Fatal(err)
	}
	st := subs.Settings{Name: "Family", Formats: []string{"uri-plain", "uri-base64"}, DefaultFormat: "uri-plain", UpdateHours: 12, ScoreNames: "claude"}
	if _, err := f.sub.Subs.Update(t.Context(), subID, access.All, st, "member:1"); err != nil {
		t.Fatal(err)
	}
	before := f.apiCalls
	f.mu.Lock()
	beforeTrace := f.traceCalls
	f.mu.Unlock()
	l := links.Link{State: "active", SubscriptionID: subID, Lang: "en"}
	for _, format := range st.Formats {
		resp, err := f.sub.Links.Serve(t.Context(), l, format, false)
		if err != nil {
			t.Fatal(err)
		}
		body := resp.Body
		if format == "uri-base64" {
			body, err = base64.StdEncoding.DecodeString(string(body))
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, line := range strings.Fields(string(body)) {
			u, err := url.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(u.Fragment, " · Claude 85") {
				t.Fatal(u.Fragment)
			}
		}
		preview, _, err := f.sub.Subs.Preview(t.Context(), subID, access.All, format, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range preview.Lines {
			_, fragment, _ := strings.Cut(line, "#")
			name, err := url.PathUnescape(fragment)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(name, " · Claude 85") || strings.Contains(line, "credential") {
				t.Fatal(line)
			}
		}
	}
	if f.apiCalls != before {
		t.Fatal("subscription did remote work")
	}
	f.mu.Lock()
	afterTrace := f.traceCalls
	f.mu.Unlock()
	if beforeTrace != afterTrace {
		t.Fatal("subscription made a remote trace request")
	}
	login := f.site.SignIn("")
	page := login.Get("/servers/" + strconv.FormatInt(f.id, 10) + "/health")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "AI services") {
		t.Fatalf("health %d", page.Code)
	}
	page = login.Get("/servers/ai?score=claude")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "85 / 100") {
		t.Fatalf("ranking %d", page.Code)
	}
	viewer := f.site.Join("viewer", access.PresetViewer.Grant(), "")
	for _, path := range []string{"/servers/ai", "/servers/" + strconv.FormatInt(f.id, 10) + "/health"} {
		if rec := viewer.Get(path); rec.Code != 403 {
			t.Fatalf("diagnostics %s: %d", path, rec.Code)
		}
	}
	if rec := viewer.Post("/servers/"+strconv.FormatInt(f.id, 10)+"/ai/run", nil); rec.Code != 403 {
		t.Fatalf("run permission %d", rec.Code)
	}
	f.setting(conf.AIEnabled, "false")
	resp, err := f.sub.Links.Serve(t.Context(), l, "uri-plain", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resp.Body), "Claude") {
		t.Fatal("disabled integration retained suffix")
	}
}

func TestUnknownCountryAndIPv6(t *testing.T) {
	f := newFixture(t)
	f.changeIP("main", "2606:4700:4700::1111")
	f.response = func(ip string) (int, string) { return 200, `{"ip":"` + ip + `","trust_score":0,"countryCode":null}` }
	f.check()
	scores := f.scores()
	if v, ok := scores["main"]["ip"]; !ok || v != 0 {
		t.Fatal("zero reputation lost")
	}
	if _, ok := scores["main"]["claude"]; ok {
		t.Fatal("unknown country became eligibility pass")
	}
	rows, _ := f.m.AI.Read(t.Context(), f.id)
	if rows[0].Attempt.Family != 6 || rows[0].CurrentIP != "2606:4700:4700::1111" {
		t.Fatal("IPv6 observation lost")
	}
	if rows[3].Attempt.Family != 4 {
		t.Fatal("IPv4 observation lost")
	}
}

func TestDefaultOffDailyQueueAndRetention(t *testing.T) {
	f := newFixture(t)
	plain := servers.New()
	sitetest.New(t, sitetest.Options{Modules: []module.Module{plain}})
	if on, err := plain.AI.Enabled(t.Context()); err != nil || on {
		t.Fatal("integration defaults on")
	}
	count := func() int {
		var n int
		if err := f.site.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE type=?`, aicheck.JobCheck); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := f.m.AI.Round(t.Context(), "system"); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("daily checks default on")
	}
	f.setting(conf.AIDaily, "true")
	for range 2 {
		if err := f.m.AI.Round(t.Context(), "system"); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatalf("daily job did not coalesce: %d", count())
	}
	if _, err := f.m.AI.Queue(t.Context(), f.id, "member:1"); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("manual queue did not coalesce")
	}
	f.check()
	f.now = f.now.Add(31 * 24 * time.Hour)
	f.setting(conf.AIEnabled, "false")
	if err := f.m.AI.Round(t.Context(), "system"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.site.App.DB.R.Get(&n, `SELECT count(*) FROM servers_ai_results`); err != nil || n != 0 {
		t.Fatalf("history retained %d %v", n, err)
	}
	if err := f.site.App.DB.R.Get(&n, `SELECT count(*) FROM servers_ai_cache`); err != nil || n != 0 {
		t.Fatalf("cache retained %d %v", n, err)
	}
	ev, err := events.List(t.Context(), f.site.App.DB.R, events.Filter{Type: "server.ai_history_pruned", Limit: 10})
	if err != nil || len(ev) != 1 {
		t.Fatalf("cleanup events %d %v", len(ev), err)
	}
	if err := f.m.AI.Round(t.Context(), "system"); err != nil {
		t.Fatal(err)
	}
	ev, _ = events.List(t.Context(), f.site.App.DB.R, events.Filter{Type: "server.ai_history_pruned", Limit: 10})
	if len(ev) != 1 {
		t.Fatal("empty cleanup emitted event")
	}
}

func TestRetirementDuringLookupDiscardsResults(t *testing.T) {
	f := newFixture(t)
	f.response = func(ip string) (int, string) {
		f.write(func(tx *sqlx.Tx) error {
			_, err := tx.Exec(`UPDATE servers_servers SET state='retired',health=NULL,retired_at=? WHERE id=?`, db.At(f.now), f.id)
			return err
		})
		return 200, `{"ip":"` + ip + `","trust_score":85,"countryCode":"nl"}`
	}
	f.check()
	var n int
	if err := f.site.App.DB.R.Get(&n, `SELECT count(*) FROM servers_ai_results`); err != nil || n != 0 {
		t.Fatalf("retirement published results: %d %v", n, err)
	}
	if _, err := f.m.AI.Queue(t.Context(), f.id, "member:1"); err != aicheck.ErrNotActive {
		t.Fatalf("retired job queued: %v", err)
	}
}

func TestTimeoutRetainsLastSuccess(t *testing.T) {
	f := newFixture(t)
	f.check()
	f.now = f.now.Add(25 * time.Hour)
	f.m.AI.HTTP = &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	f.check()
	rows, err := f.m.AI.Read(t.Context(), f.id)
	if err != nil || rows[0].Attempt.Error != "reputation request failed" || rows[0].Success == nil || !rows[0].Fresh {
		t.Fatalf("timeout lost previous success: %+v %v", rows, err)
	}
}

func TestRankingOrdersKnownBeforeUnknownAndScoresDescending(t *testing.T) {
	f := newFixture(t)
	f.response = func(ip string) (int, string) {
		score := 30
		if ip == "1.1.1.1" {
			score = 90
		}
		return 200, fmt.Sprintf(`{"ip":%q,"trust_score":%d,"countryCode":"nl"}`, ip, score)
	}
	f.check()
	login := f.site.SignIn("")
	assertOrder := func(first, second string) {
		t.Helper()
		rec := login.Get("/servers/ai?score=ip")
		body := rec.Body.String()
		a, b := strings.Index(body, ">"+first+" · Claude<"), strings.Index(body, ">"+second+" · Claude<")
		if rec.Code != 200 || a < 0 || b < 0 || a >= b {
			t.Fatalf("unexpected ranking %d: %d, %d", rec.Code, a, b)
		}
	}
	assertOrder("extra", "main")
	f.now = f.now.Add(25 * time.Hour)
	f.response = func(ip string) (int, string) {
		country := "nl"
		score := 30
		if ip == "1.1.1.1" {
			country = ""
			score = 90
		}
		return 200, fmt.Sprintf(`{"ip":%q,"trust_score":%d,"countryCode":%q}`, ip, score, country)
	}
	f.check()
	assertOrder("main", "extra")
	body := login.Get("/servers/ai").Body.String()
	if strings.Contains(body, "main-credential") || strings.Contains(body, "secret-main") {
		t.Fatal("ranking revealed endpoint secrets")
	}
}

func TestConcurrentChecksSharePublishedCache(t *testing.T) {
	f := newFixture(t)
	f.changeIP("extra", "8.8.8.8")
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- f.m.AI.Check(t.Context(), f.id, "member:1") }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if f.apiCalls != 1 {
		t.Fatalf("concurrent checks used %d lookups", f.apiCalls)
	}
}

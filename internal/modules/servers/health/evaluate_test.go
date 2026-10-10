package health_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

var now0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func passP(fb, kbps int) health.ProxyResult {
	return health.ProxyResult{At: now0, OK: true, FirstByteMS: fb, Kbps: kbps}
}

func failP() health.ProxyResult {
	return health.ProxyResult{At: now0, Class: "stalled", Error: "no data for 5s after 16 KB received"}
}

func ext(ru, abroad int) *health.ExternalResult {
	e := &health.ExternalResult{At: now0}
	for i := 0; i < 3; i++ {
		e.Nodes = append(e.Nodes, health.NodeAnswer{Node: fmt.Sprintf("ru%d", i), Country: "ru", OK: i < ru})
		e.Nodes = append(e.Nodes, health.NodeAnswer{Node: fmt.Sprintf("de%d", i), Country: []string{"de", "nl", "fi"}[i], Abroad: true, OK: i < abroad})
	}
	return e
}

func self(class string, failing ...string) *health.SelfResult {
	s := &health.SelfResult{At: now0, Class: class}
	for _, f := range failing {
		s.Failures = append(s.Failures, health.Failure{Kind: "compose-running", Message: f})
	}
	return s
}

func eval(in health.CheckState) health.Outcome {
	return health.Evaluate(in, health.DefaultThresholds, now0)
}

func one(p health.ProxyResult, s *health.SelfResult, e *health.ExternalResult) health.CheckState {
	return health.CheckState{Endpoints: []string{"main"}, Proxy: map[string]health.ProxyResult{"main": p}, Self: s, External: e, Home: health.HomeOnline}
}

// TestEveryInputHasAVerdict enumerates the proxy, self-check and external
// combinations and checks each against the rule the process doc names for it.
func TestEveryInputHasAVerdict(t *testing.T) {
	type proxyCase struct {
		name string
		in   map[string]health.ProxyResult
	}
	proxies := []proxyCase{
		{"all pass", map[string]health.ProxyResult{"a": passP(200, 8000), "b": passP(300, 5000)}},
		{"slow", map[string]health.ProxyResult{"a": passP(2400, 8000), "b": passP(300, 5000)}},
		{"some fail", map[string]health.ProxyResult{"a": passP(200, 8000), "b": failP()}},
		{"all fail", map[string]health.ProxyResult{"a": failP(), "b": failP()}},
		{"missing", map[string]health.ProxyResult{"a": passP(200, 8000)}},
	}
	selfs := map[string]*health.SelfResult{
		"ok": self(health.SelfOK), "stack": self(health.SelfStackFailed, "xray: exited"), "unreachable": self(health.SelfUnreachable),
		"missing": nil, "hostkey": self(health.SelfHostKey),
	}
	exts := map[string]*health.ExternalResult{"none": nil, "zero": ext(0, 0), "abroad": ext(0, 2)}

	// want is the table of docs/processes/servers/server-health.md#verdict,
	// written out.
	want := func(p, s, e string) (rule, cand string) {
		switch {
		case s == "hostkey":
			return "2a", health.Unknown
		case p == "missing":
			return "3", health.Unknown
		case p == "all pass" && (s == "ok" || s == "missing"):
			return "4", health.Healthy
		case p == "all pass" && s == "unreachable":
			return "5a", health.Degraded
		case p != "all fail":
			return "5", health.Degraded
		case s == "stack":
			return "6", health.Down
		case e == "abroad":
			return "7", health.Blocked
		case s == "ok" && e == "none":
			return "8", health.Blocked
		case s == "ok":
			return "8a", health.Down
		case s == "unreachable" && e == "zero":
			return "9", health.Down
		case s == "unreachable":
			return "10", health.Down
		}
		return "11", health.Unknown
	}
	n := 0
	for _, p := range proxies {
		for sn, s := range selfs {
			for en, e := range exts {
				in := health.CheckState{Endpoints: []string{"a", "b"}, Proxy: p.in, Self: s, External: e, Home: health.HomeOnline}
				o := eval(in)
				rule, cand := want(p.name, sn, en)
				if o.Rule != rule || o.Candidate != cand {
					t.Errorf("proxy %s, self %s, external %s: rule %s → %s, want %s → %s", p.name, sn, en, o.Rule, o.Candidate, rule, cand)
				}
				if cand != health.Healthy && o.Reason.Key == "" {
					t.Errorf("proxy %s, self %s, external %s: no reason", p.name, sn, en)
				}
				if o.Frozen || o.Wait {
					t.Errorf("proxy %s, self %s, external %s: frozen or waiting without a cause", p.name, sn, en)
				}
				n++
			}
		}
	}
	if n != 75 {
		t.Fatalf("%d combinations", n)
	}
	// the rules above every other input
	if o := eval(health.CheckState{Paused: true, Home: health.HomeOffline}); o.Rule != "1" || !o.Immediate || o.Candidate != health.Paused {
		t.Errorf("paused: %+v", o)
	}
	if o := eval(health.CheckState{Home: health.HomeOffline, Endpoints: []string{"a"}}); o.Rule != "2" || !o.Frozen {
		t.Errorf("offline: %+v", o)
	}
	if o := eval(health.CheckState{Home: health.HomeForeignUnreachable}); !o.Frozen {
		t.Errorf("foreign unreachable: %+v", o)
	}
	// an external result without a node abroad cannot confirm anything
	if o := eval(one(failP(), self(health.SelfOK), &health.ExternalResult{At: now0, Nodes: []health.NodeAnswer{{Node: "ru1", OK: true}}})); o.Rule != "8" {
		t.Errorf("external with only Russian nodes: rule %s", o.Rule)
	}
}

func TestProxyWorksWithoutSSHIsDegraded(t *testing.T) {
	o := eval(one(passP(200, 8000), self(health.SelfUnreachable), nil))
	if o.Candidate != health.Degraded || o.Rule != "5a" {
		t.Fatalf("%+v", o)
	}
	loc := newCatalogLocalizer(t)
	if got := health.RenderReason(loc, o.Reason); !strings.Contains(got, "SSH is unreachable") {
		t.Errorf("reason %q", got)
	}
}

func TestStackRunsButUnreachableIsDown(t *testing.T) {
	o := eval(one(failP(), self(health.SelfOK), ext(0, 0)))
	if o.Candidate != health.Down || o.Rule != "8a" {
		t.Fatalf("%+v", o)
	}
	if got := health.RenderReason(newCatalogLocalizer(t), o.Reason); !strings.Contains(got, "The stack runs, but") || !strings.Contains(got, "3/3 nodes abroad") {
		t.Errorf("reason %q", got)
	}
}

func TestNotEnoughDataIsUnknown(t *testing.T) {
	o := eval(one(failP(), nil, nil))
	if o.Candidate != health.Unknown || o.Rule != "11" {
		t.Fatalf("%+v", o)
	}
	if got := health.RenderReason(newCatalogLocalizer(t), o.Reason); !strings.Contains(got, "Not enough data") {
		t.Errorf("reason %q", got)
	}
}

func TestDegradedSlow(t *testing.T) {
	o := eval(one(passP(2400, 8000), self(health.SelfOK), nil))
	if o.Candidate != health.Degraded || o.Rule != "5" {
		t.Fatalf("%+v", o)
	}
	if got := health.RenderReason(newCatalogLocalizer(t), o.Reason); got != "Slow: 2.4 s to first byte" {
		t.Errorf("reason %q", got)
	}
	// and by throughput
	o = eval(one(passP(200, 500), self(health.SelfOK), nil))
	if o.Candidate != health.Degraded || !strings.Contains(health.RenderReason(newCatalogLocalizer(t), o.Reason), "500 kbit/s") {
		t.Errorf("slow throughput: %+v", o)
	}
	// no notification: degraded never notifies
	f := newFixture(t)
	f.Healthy()
	f.Tick(5 * time.Minute)
	f.Proxy("main", true, "", "", 2400, 8000)
	f.Eval()
	f.Tick(5 * time.Minute)
	f.Proxy("main", true, "", "", 2400, 8000)
	f.Eval()
	if got := f.Health().Health; got != health.Degraded {
		t.Fatalf("health %s", got)
	}
	if n := f.Notifying("server.health_changed"); n != 0 {
		t.Errorf("%d notifications for a slow server", n)
	}
}

func TestDegradedStackPart(t *testing.T) {
	o := eval(one(passP(200, 8000), self(health.SelfStackFailed, "certbot: exited"), nil))
	if o.Candidate != health.Degraded {
		t.Fatalf("%+v", o)
	}
	if got := health.RenderReason(newCatalogLocalizer(t), o.Reason); got != "certbot container not running" {
		t.Errorf("reason %q", got)
	}
}

func TestDegradedOneEndpoint(t *testing.T) {
	in := health.CheckState{Endpoints: []string{"main", "backup"}, Home: health.HomeOnline, Self: self(health.SelfOK),
		Proxy: map[string]health.ProxyResult{"main": passP(200, 8000), "backup": failP()}}
	o := eval(in)
	if o.Candidate != health.Degraded {
		t.Fatalf("%+v", o)
	}
	if got := health.RenderReason(newCatalogLocalizer(t), o.Reason); !strings.HasPrefix(got, "Endpoint backup fails") {
		t.Errorf("reason %q", got)
	}
	// and through the service, with a second endpoint on the server
	f := newFixture(t)
	f.AddEndpoint("backup")
	f.Self(health.SelfOK)
	f.ProxyOK("main")
	f.ProxyFail("backup", "stalled", "no data")
	f.Eval()
	if h := f.Health(); h.Health != health.Degraded {
		t.Fatalf("health %s", h.Health)
	}
	if !strings.Contains(f.ReasonText(), "Endpoint backup fails") {
		t.Errorf("reason %q", f.ReasonText())
	}
}

func TestFirstStateAfterUnknownIsImmediate(t *testing.T) {
	f := newFixture(t)
	if got := f.Health().Health; got != health.Unknown {
		t.Fatalf("a new server is %s", got)
	}
	f.Self(health.SelfOK)
	f.ProxyOK("main")
	f.Eval()
	h := f.Health()
	if h.Health != health.Healthy || h.CandidateCount != 0 {
		t.Fatalf("health %s count %d: the first state after unknown applies at once", h.Health, h.CandidateCount)
	}
	if c := f.Changes(); len(c) != 1 || c[0].Payload["from"] != "unknown" || c[0].Payload["to"] != "healthy" {
		t.Fatalf("events %+v", c)
	}
	// coming from unknown, healthy does not notify
	if n := f.Notifying("server.health_changed"); n != 0 {
		t.Errorf("%d notifications", n)
	}
}

func TestOneFailureDoesNotFlip(t *testing.T) {
	f := newFixture(t)
	f.Healthy()

	f.Tick(5 * time.Minute)
	f.ProxyFail("main", "stalled", "no data for 5s after 16 KB received")
	f.Eval()
	if h := f.Health(); h.Health != health.Healthy || h.CandidateCount != 1 {
		t.Fatalf("after one failure: %s count %d", h.Health, h.CandidateCount)
	}
	// the round's self-check looks at the same failed result: it does not count
	f.Tick(150 * time.Second)
	f.Self(health.SelfOK)
	f.Eval()
	if h := f.Health(); h.Health != health.Healthy || h.CandidateCount != 1 {
		t.Fatalf("after the self-check: %s count %d", h.Health, h.CandidateCount)
	}
	f.Tick(5 * time.Minute)
	f.ProxyOK("main")
	f.Eval()
	if h := f.Health(); h.Health != health.Healthy || h.CandidateCount != 0 {
		t.Fatalf("after a pass: %s count %d", h.Health, h.CandidateCount)
	}
	if n := len(f.Changes()); n != 1 {
		t.Errorf("%d changes: only the first state", n)
	}
}

func TestBlockedAfterTwoEvaluations(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	for i := 0; i < 2; i++ {
		f.Tick(5 * time.Minute)
		f.Self(health.SelfOK)
		f.External([]bool{false, false, false}, []bool{true, true, false})
		f.ProxyFail("main", "stalled", "no data for 5s after 16 KB received")
		f.Eval()
		if i == 0 {
			if h := f.Health(); h.Health != health.Healthy || h.CandidateCount != 1 {
				t.Fatalf("after one: %s count %d", h.Health, h.CandidateCount)
			}
		}
	}
	if h := f.Health(); h.Health != health.Blocked {
		t.Fatalf("health %s", h.Health)
	}
	text := f.ReasonText()
	if !strings.Contains(text, "stalled") || !strings.Contains(text, "16 KB") || !strings.Contains(text, "Germany and Netherlands") || !strings.Contains(text, "0/3 Russian nodes connect") {
		t.Errorf("reason %q", text)
	}
	if n := f.Notifying("server.health_changed"); n != 1 {
		t.Errorf("%d notifications, want 1", n)
	}
	msg, ok, err := f.Mod.RenderNotification(bg, f.Changes()[1], f.App.I18n.Localizer(i18n.EN, nil))
	if err != nil || !ok || msg.Emoji != "🟣" || !strings.Contains(msg.Title, "is blocked") || !strings.Contains(msg.Body, "16 KB") {
		t.Errorf("notification %+v %v %v", msg, ok, err)
	}
}

func TestBlockedUnconfirmed(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	for i := 0; i < 2; i++ {
		f.Tick(5 * time.Minute)
		f.Self(health.SelfOK)
		f.ProxyFail("main", "stalled", "no data")
		f.Eval()
	}
	if h := f.Health(); h.Health != health.Blocked {
		t.Fatalf("health %s", h.Health)
	}
	if !strings.Contains(f.ReasonText(), "unconfirmed from abroad") {
		t.Errorf("reason %q", f.ReasonText())
	}
}

func TestDownUnreachable(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	for i := 0; i < 2; i++ {
		f.Tick(5 * time.Minute)
		f.Self(health.SelfUnreachable)
		f.External([]bool{false, false, false}, []bool{false, false, false})
		f.ProxyFail("main", "tcp-timeout", "dial tcp: i/o timeout")
		f.Eval()
	}
	if h := f.Health(); h.Health != health.Down {
		t.Fatalf("health %s", h.Health)
	}
	if !strings.Contains(f.ReasonText(), "Unreachable from home and from 3/3 nodes abroad") {
		t.Errorf("reason %q", f.ReasonText())
	}
}

func TestDownStackBroken(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	for i := 0; i < 2; i++ {
		f.Tick(5 * time.Minute)
		f.Self(health.SelfStackFailed, health.Failure{Kind: "compose-running", Message: "xray: exited"})
		f.ProxyFail("main", "http-error", "EOF")
		f.Eval()
	}
	if h := f.Health(); h.Health != health.Down {
		t.Fatalf("health %s", h.Health)
	}
	if !strings.Contains(f.ReasonText(), "Stack broken: xray container not running") {
		t.Errorf("reason %q", f.ReasonText())
	}
}

func TestHostKeyChangedIsUnknown(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	f.Tick(time.Minute)
	f.Self(health.SelfHostKey)
	f.ProxyOK("main")
	f.Eval()
	if h := f.Health(); h.Health != health.Unknown {
		t.Fatalf("health %s: a changed host key applies at once", h.Health)
	}
	if !strings.Contains(f.ReasonText(), "Host key changed") {
		t.Errorf("reason %q", f.ReasonText())
	}
	for _, c := range f.Changes() {
		if c.Payload["to"] == "down" {
			t.Error("down is never the verdict of a changed key")
		}
	}
	if n := f.Notifying("server.health_changed"); n != 0 {
		t.Errorf("%d health notifications: the security notification is ssh.host_key_changed", n)
	}
}

func TestHealthNotificationRule(t *testing.T) {
	f := newFixture(t)
	typ, ok := f.App.Events.Lookup("server.health_changed")
	if !ok {
		t.Fatal("not declared")
	}
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"healthy", "blocked", true}, {"degraded", "down", true}, {"blocked", "healthy", true}, {"down", "healthy", true},
		{"unknown", "healthy", false}, {"degraded", "healthy", false}, {"healthy", "degraded", false}, {"healthy", "unknown", false},
		{"healthy", "paused", false}, {"paused", "unknown", false},
	} {
		if got := typ.NotifyIf(map[string]any{"from": c.from, "to": c.to}); got != c.want {
			t.Errorf("%s → %s notifies %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestHomeOfflineFreezes(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	before := len(f.Changes())
	f.SetRef("/dom", false)
	f.Tick(time.Minute)
	f.runJob(health.JobReference)
	if h, _, _ := homeOf(f); h != "offline" {
		t.Fatalf("home %s", h)
	}
	if len(f.Events("health.home_offline")) != 1 {
		t.Errorf("home_offline events: %d", len(f.Events("health.home_offline")))
	}
	// failing results while offline are inconclusive and change nothing
	for i := 0; i < 3; i++ {
		f.Tick(5 * time.Minute)
		f.Self(health.SelfUnreachable)
		f.ProxyFail("main", "tcp-timeout", "timeout")
		f.Eval()
	}
	if h := f.Health(); h.Health != health.Healthy || h.CandidateCount != 0 {
		t.Fatalf("health %s count %d while home is offline", h.Health, h.CandidateCount)
	}
	if len(f.Changes()) != before || f.Notifying("server.health_changed") != 0 {
		t.Errorf("a server changed while home was offline")
	}
	if n := f.Notifying("health.home_offline"); n != 0 {
		t.Errorf("home_offline must not notify")
	}
}

func TestForeignUnreachableAndRecovery(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	f.SetRef("/for", false)
	f.runJob(health.JobReference)
	f.runJob(health.JobReference) // the same state again records nothing more
	if h, _, _ := homeOf(f); h != "foreign-unreachable" {
		t.Fatalf("home %s", h)
	}
	if n := len(f.Events("health.foreign_unreachable")); n != 1 {
		t.Fatalf("%d foreign_unreachable events", n)
	}
	if f.Notifying("health.foreign_unreachable") != 1 {
		t.Error("foreign_unreachable should notify")
	}
	f.Tick(12 * time.Minute)
	f.SetRef("/for", true)
	f.runJob(health.JobReference)
	if h, _, _ := homeOf(f); h != "online" {
		t.Fatalf("home %s", h)
	}
	rec := f.Events("health.home_recovered")
	if len(rec) != 1 || rec[0].Payload["duration"] != "12m0s" {
		t.Fatalf("recovered %+v", rec)
	}
	if f.Notifying("health.home_recovered") != 1 {
		t.Error("home_recovered should notify")
	}
}

func TestRecoveryNeedsTwoFreshEvaluations(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	// one failure before the outage is a candidate that must not carry over it
	f.Tick(5 * time.Minute)
	f.Self(health.SelfOK)
	f.ProxyFail("main", "stalled", "x")
	f.Eval()
	if f.Health().CandidateCount != 1 {
		t.Fatal("no candidate before the outage")
	}
	f.SetRef("/dom", false)
	f.runJob(health.JobReference)
	// results during the outage are inconclusive: they must not count
	f.Tick(5 * time.Minute)
	f.ProxyFail("main", "tcp-timeout", "outage")
	f.Self(health.SelfUnreachable)
	f.Eval()
	f.SetRef("/dom", true)
	f.Tick(time.Minute)
	f.runJob(health.JobReference)
	if h := f.Health(); h.CandidateCount != 0 {
		t.Fatalf("the candidate run survived the outage: %d", h.CandidateCount)
	}
	// the first fresh failing evaluation is only a candidate, the second changes
	f.Tick(5 * time.Minute)
	f.Self(health.SelfOK)
	f.ProxyFail("main", "stalled", "x")
	f.Eval()
	if h := f.Health(); h.Health != health.Healthy || h.CandidateCount != 1 {
		t.Fatalf("first after recovery: %s count %d", h.Health, h.CandidateCount)
	}
	f.Tick(5 * time.Minute)
	f.Self(health.SelfOK)
	f.ProxyFail("main", "stalled", "x")
	f.Eval()
	if h := f.Health(); h.Health != health.Blocked {
		t.Fatalf("second after recovery: %s", h.Health)
	}
}

func TestStillUnhealthyReminder(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	for i := 0; i < 2; i++ {
		f.Tick(5 * time.Minute)
		f.Self(health.SelfOK)
		f.ProxyFail("main", "stalled", "x")
		f.Eval()
	}
	if f.Health().Health != health.Blocked {
		t.Fatal("not blocked")
	}
	f.Tick(23 * time.Hour)
	f.runJob(health.JobReminders)
	if n := len(f.Events("server.still_unhealthy")); n != 0 {
		t.Fatalf("%d reminders after 23 h", n)
	}
	f.Tick(2 * time.Hour) // 25 h in all
	f.runJob(health.JobReminders)
	f.runJob(health.JobReminders) // not again at once
	r := f.Events("server.still_unhealthy")
	if len(r) != 1 || r[0].Payload["state"] != "blocked" {
		t.Fatalf("reminders %+v", r)
	}
	if f.Notifying("server.still_unhealthy") != 1 {
		t.Error("the reminder should notify")
	}
}

func TestCertExpiringOncePerCrossing(t *testing.T) {
	f := newFixture(t)
	days := func(n int) { f.VPS.CertDaysLeft = n }
	count := func() int { return len(f.Events("server.cert_expiring")) }

	days(10)
	f.selfcheck()
	if count() != 1 || f.Events("server.cert_expiring")[0].Payload["days_left"] != float64(10) {
		t.Fatalf("events %+v", f.Events("server.cert_expiring"))
	}
	f.selfcheck() // still under 14 days: no second one
	if count() != 1 {
		t.Fatalf("%d events while still under the threshold", count())
	}
	// the self-check passes: an expiring certificate is a warning, not a failure
	if l := latestSelf(t, f); l.Class != health.SelfOK {
		t.Errorf("self-check %s", l.Class)
	}
	days(89) // renewed
	f.selfcheck()
	if count() != 1 {
		t.Fatalf("renewing raised an event: %d", count())
	}
	if h := f.Health(); h.CertWarned {
		t.Error("the flag stayed set after renewal")
	}
	days(9) // under again: a new crossing
	f.selfcheck()
	if count() != 2 {
		t.Fatalf("%d events after crossing again", count())
	}
	if f.Notifying("server.cert_expiring") != 2 {
		t.Error("cert_expiring should notify")
	}
}

func TestDiskLowOncePerCrossing(t *testing.T) {
	f := newFixture(t)
	count := func() int { return len(f.Events("server.disk_low")) }

	f.VPS.DiskFreePct = 8
	f.selfcheck()
	if count() != 1 || f.Events("server.disk_low")[0].Payload["free_pct"] != float64(8) {
		t.Fatalf("events %+v", f.Events("server.disk_low"))
	}
	f.selfcheck()
	if count() != 1 {
		t.Fatalf("%d events while still low", count())
	}
	f.VPS.DiskFreePct = 60 // recovers
	f.selfcheck()
	f.VPS.DiskFreePct = 7 // and drops again
	f.selfcheck()
	if count() != 2 {
		t.Fatalf("%d events after the second drop", count())
	}
}

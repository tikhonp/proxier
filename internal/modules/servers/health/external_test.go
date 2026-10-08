package health_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/checkhost/checkhosttest"
	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func (f *fixture) setNodes() {
	f.T.Helper()
	if err := f.App.Settings.Set(bg, "admin", "servers", map[string]string{
		"servers.external_nodes_ru":     "ru1.node.check-host.net,ru2.node.check-host.net,ru3.node.check-host.net",
		"servers.external_nodes_abroad": "de1.node.check-host.net,nl1.node.check-host.net,se1.node.check-host.net",
	}); err != nil {
		f.T.Fatal(err)
	}
}

func (f *fixture) queueExternal(kind string) {
	f.T.Helper()
	if _, err := f.App.Jobs.EnqueueNow(bg, jobs.Request{Type: health.JobExternal, Subject: store.ServerSubject(f.ID), CreatedBy: "test",
		Payload: map[string]any{"server_id": f.ID, "kind": kind}}); err != nil {
		f.T.Fatal(err)
	}
}

func (f *fixture) externalRows(class string) int {
	f.T.Helper()
	var n int
	if err := f.App.DB.R.Get(&n, `SELECT count(*) FROM servers_check_results WHERE kind = 'external' AND class = ?`, class); err != nil {
		f.T.Fatal(err)
	}
	return n
}

// TestOnDemandExternalIsAwaited: a failing proxy test with an external result
// 25 minutes old asks for a new one, and the verdict waits for it.
func TestOnDemandExternalIsAwaited(t *testing.T) {
	f := newFixture(t)
	f.setNodes()
	f.Healthy()
	host := f.Server(f.ID).IP + ":443"
	f.CH.Set(host, "de1.node.check-host.net", checkhosttest.Answer{MS: 30})
	f.CH.Set(host, "nl1.node.check-host.net", checkhosttest.Answer{MS: 30})
	f.CH.Set(host, "ru1.node.check-host.net", checkhosttest.Answer{Error: "Connection timed out"})
	f.CH.Set(host, "ru2.node.check-host.net", checkhosttest.Answer{Error: "Connection timed out"})
	f.CH.Set(host, "ru3.node.check-host.net", checkhosttest.Answer{Error: "Connection timed out"})

	f.Tick(25 * time.Minute)
	f.External([]bool{true, true, true}, []bool{true, true, true}) // 25 min old by the time it is needed
	f.Tick(25 * time.Minute)
	f.Self(health.SelfOK)

	// A failing proxy round: the job stores it, asks for an external check and
	// the evaluation waits.
	f.VPS.SetService("nginx", "running")
	f.Harness.StallSmoke(true)
	f.queueProxytest()
	f.WaitFor("the proxy test", func() bool { return f.countResults("proxy") == 2 })
	f.Drain()

	// The external check ran, and its own evaluation made the first candidate.
	if len(f.CH.Checks()) != 1 {
		t.Fatalf("%d check-host requests", len(f.CH.Checks()))
	}
	if h := f.Health(); health.ParseDetail(h.Detail).Awaiting != "" {
		t.Errorf("still awaiting: %s", h.Detail)
	}
	if h := f.Health(); h.Candidate.String != health.Blocked || h.CandidateCount != 1 {
		t.Fatalf("candidate %v count %d: the external check's evaluation is the counted one", h.Candidate, h.CandidateCount)
	}
	var queued int
	if err := f.App.DB.R.Get(&queued, `SELECT count(*) FROM jobs WHERE type = ? AND state = 'queued'`, health.JobEvaluate); err != nil || queued != 0 {
		t.Errorf("%d delayed evaluations still queued after the external check answered (%v)", queued, err)
	}
}

// TestOnDemandWaitsWhenNothingAnswers: the evaluation stores nothing while the
// external check is on its way, and looks again after EvalDelay.
func TestOnDemandWaitsWhenNothingAnswers(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	f.Tick(5 * time.Minute)
	f.Self(health.SelfOK)
	f.ProxyFail("main", "stalled", "x")
	// the proxy test job marked the wait
	if err := f.App.DB.Write(bg, func(tx *sqlx.Tx) error { return f.Svc.MarkAwaiting(bg, tx, f.ID, f.Clock.Now()) }); err != nil {
		t.Fatal(err)
	}
	before := f.Health()
	f.Eval()
	after := f.Health()
	if after.CandidateCount != before.CandidateCount || after.Candidate != before.Candidate {
		t.Fatalf("a waiting evaluation stored a candidate: %+v", after)
	}
	if f.countJobs(health.JobEvaluate) != 1 {
		t.Fatalf("no delayed evaluation was queued")
	}
	// after two minutes without an answer the evaluation decides without it
	f.Tick(3 * time.Minute)
	f.Self(health.SelfOK)
	f.ProxyFail("main", "stalled", "x")
	f.Eval()
	if h := f.Health(); h.Candidate.String != health.Blocked {
		t.Fatalf("candidate %v", h.Candidate)
	}
}

func TestCheckHostErrorIsUnconfirmed(t *testing.T) {
	f := newFixture(t)
	f.setNodes()
	f.Healthy()
	f.CH.Fail(503)
	for i := 0; i < 2; i++ {
		f.Tick(5 * time.Minute)
		f.Self(health.SelfOK)
		f.ProxyFail("main", "stalled", "x")
		f.queueExternal(health.ExternalOnDemand)
		f.Drain()
	}
	if n := f.externalRows("unavailable"); n == 0 {
		t.Fatal("no unavailable row")
	}
	// the evaluation after the failed check has no external data
	f.Eval()
	if h := f.Health(); h.Health != health.Blocked {
		t.Fatalf("health %s", h.Health)
	}
	if !strings.Contains(f.ReasonText(), "unconfirmed from abroad") {
		t.Errorf("reason %q", f.ReasonText())
	}
}

func TestExternalBudget(t *testing.T) {
	f := newFixture(t)
	f.setNodes()
	f.Healthy()

	// a first on-demand check runs
	f.queueExternal(health.ExternalOnDemand)
	f.Drain()
	if n := f.externalRows("skipped"); n != 0 || len(f.CH.Checks()) != 1 {
		t.Fatalf("first: %d skipped, %d requests", n, len(f.CH.Checks()))
	}
	// a second one within 10 minutes is skipped and recorded
	f.Tick(5 * time.Minute)
	f.queueExternal(health.ExternalOnDemand)
	f.Drain()
	if n := f.externalRows("skipped"); n != 1 || len(f.CH.Checks()) != 1 {
		t.Fatalf("second: %d skipped, %d requests", n, len(f.CH.Checks()))
	}
	// a scheduled check 29 min 50 s after the first result still runs: the
	// per-server limit is 80 % of the interval
	f.Tick(24*time.Minute + 50*time.Second)
	f.queueExternal(health.ExternalScheduled)
	f.Drain()
	if len(f.CH.Checks()) != 2 {
		t.Fatalf("the scheduled check did not run: %d requests", len(f.CH.Checks()))
	}
	// the hourly cap: with it reached, a due check is skipped
	if err := f.App.Settings.Set(bg, "admin", "servers", map[string]string{"servers.external_hourly_cap": "2"}); err != nil {
		t.Fatal(err)
	}
	f.Tick(30 * time.Minute)
	f.queueExternal(health.ExternalScheduled)
	f.Drain()
	if len(f.CH.Checks()) != 2 || f.externalRows("skipped") != 2 {
		t.Fatalf("over the cap: %d requests, %d skipped", len(f.CH.Checks()), f.externalRows("skipped"))
	}
	// skipped rows are not data: the verdict has no external result
	st, err := f.Svc.Load(bg, f.ID, f.Clock.Now())
	if err != nil || st.External != nil {
		t.Fatalf("external %v %v", st.External, err)
	}
}

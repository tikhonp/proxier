package health_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func (f *fixture) resumeJobs() []jobs.Job {
	f.T.Helper()
	l, err := f.App.Jobs.List(bg, jobs.Filter{Type: health.JobResume, Limit: 50})
	if err != nil {
		f.T.Fatal(err)
	}
	return l
}

func TestPauseForAnHour(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	if err := f.Svc.Pause(bg, []int64{f.ID}, time.Hour, false, "admin"); err != nil {
		t.Fatal(err)
	}
	h := f.Health()
	if h.Health != health.Paused || h.PausedUntil.IsZero() {
		t.Fatalf("health %s until %v: paused at once", h.Health, h.PausedUntil)
	}
	if n := len(f.Events("server.checks_paused")); n != 1 {
		t.Errorf("%d checks_paused events", n)
	}
	// no checks for an hour: a round skips the server
	if _, err := f.App.Jobs.EnqueueNow(bg, jobsRequest(health.JobRound)); err != nil {
		t.Fatal(err)
	}
	f.waitJob(health.JobRound)
	if n := f.countJobs(health.JobSelfcheck); n != 0 {
		t.Fatalf("%d self-checks queued for a paused server", n)
	}
	// results that arrive anyway change nothing
	f.Self(health.SelfUnreachable)
	f.ProxyFail("main", "tcp-timeout", "x")
	f.Eval()
	if f.Health().Health != health.Paused {
		t.Fatal("an evaluation moved a paused server")
	}
	// the end of the hour
	f.Tick(time.Hour + time.Second)
	f.WaitFor("the pause to end", func() bool { return len(f.Events("server.checks_resumed")) == 1 })
	var toUnknown bool
	for _, c := range f.Changes() {
		toUnknown = toUnknown || (c.Payload["from"] == "paused" && c.Payload["to"] == "unknown")
	}
	if !toUnknown {
		t.Error("the pause did not end in unknown")
	}
	// a check round follows at once, and its results give the verdict
	f.Drain()
	if f.countJobs(health.JobSelfcheck) != 1 || f.countJobs(health.JobProxytest) != 1 {
		t.Errorf("the round after the pause: %d self-checks, %d proxy tests", f.countJobs(health.JobSelfcheck), f.countJobs(health.JobProxytest))
	}
	h = f.Health()
	if !h.PausedUntil.IsZero() || h.Health != health.Healthy {
		t.Errorf("health %s, paused until %v: a verdict from the new results", h.Health, h.PausedUntil)
	}
}

func TestManualResumeCancelsResumeJob(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	if err := f.Svc.Pause(bg, []int64{f.ID}, 24*time.Hour, false, "admin"); err != nil {
		t.Fatal(err)
	}
	first := f.resumeJobs()
	if len(first) != 1 || first[0].State != jobs.Queued {
		t.Fatalf("resume jobs %+v", first)
	}
	if err := f.Svc.Resume(bg, f.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if j, _ := f.App.Jobs.Job(bg, first[0].ID); j.State != jobs.Cancelled {
		t.Fatalf("the old resume job is %s", j.State)
	}
	if n := len(f.Events("server.checks_resumed")); n != 1 {
		t.Fatalf("%d checks_resumed events", n)
	}
	f.Drain()
	if got := f.Health().Health; got != health.Healthy {
		t.Fatalf("health %s: after a manual resume the new results decide", got)
	}

	if err := f.Svc.Pause(bg, []int64{f.ID}, time.Hour, false, "admin"); err != nil {
		t.Fatal(err)
	}
	// after an hour the new pause ends, not after 24
	f.Tick(time.Hour + time.Second)
	f.WaitFor("the one hour pause to end", func() bool { return len(f.Events("server.checks_resumed")) == 2 })
	f.Drain()
	if got := f.Health().Health; got != health.Healthy {
		t.Fatalf("health %s", got)
	}
	// and a pause begun meanwhile is not cut short by a job nobody cancelled
	if err := f.Svc.Pause(bg, []int64{f.ID}, 6*time.Hour, false, "admin"); err != nil {
		t.Fatal(err)
	}
	f.Tick(time.Hour)
	time.Sleep(60 * time.Millisecond) // nothing is due: give a wrong job the chance to run
	if got := f.Health().Health; got != health.Paused {
		t.Fatalf("health %s: only the resume job of the current pause may end it", got)
	}
	// a resume job that runs when the stored end is later does nothing
	if _, err := f.App.Jobs.EnqueueNow(bg, jobs.Request{Type: health.JobResume, CreatedBy: "test",
		Subject: store.ServerSubject(f.ID), Payload: map[string]any{"server_id": f.ID}}); err != nil {
		t.Fatal(err)
	}
	if j := f.waitJob(health.JobResume); j.State != jobs.Succeeded {
		t.Fatalf("resume job %s", j.State)
	}
	if got := f.Health().Health; got != health.Paused {
		t.Fatalf("a stale resume job ended the pause: %s", got)
	}
}

package health_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// selfcheck runs the self-check job of the server against the fake VPS.
func (f *fixture) selfcheck() {
	f.T.Helper()
	f.Tick(time.Second) // results of one instant are one batch
	if _, err := f.App.Jobs.EnqueueNow(bg, jobs.Request{
		Type: health.JobSelfcheck, ResourceKey: "server:" + sid(f.ID), CreatedBy: "test", Payload: map[string]any{"server_id": f.ID},
	}); err != nil {
		f.T.Fatal(err)
	}
	f.Drain()
}

func latestSelf(t *testing.T, f *fixture) store.CheckResult {
	t.Helper()
	rs, err := store.LatestResults(bg, f.App.DB.R, f.ID, "self")
	if err != nil || len(rs) != 1 {
		t.Fatalf("self results %v %v", rs, err)
	}
	return rs[0]
}

func TestSelfCheckStoresResultAndSample(t *testing.T) {
	f := newFixture(t)
	f.selfcheck()
	if l := latestSelf(t, f); l.Class != health.SelfOK || !l.OK || l.Inconclusive {
		t.Fatalf("result %+v", l)
	}
	s, ok, err := store.LastSample(bg, f.App.DB.R, f.ID)
	if err != nil || !ok || s.MemTotal == 0 || s.CPUPct.Valid {
		t.Fatalf("sample %+v %v %v", s, ok, err)
	}

	// a stopped container: the checks fail, and the class says so
	f.VPS.SetService("certbot", "exited")
	f.selfcheck()
	if l := latestSelf(t, f); l.Class != health.SelfStackFailed || l.OK {
		t.Fatalf("result %+v", l)
	}
	f.VPS.SetService("certbot", "running")

	// no SSH: unreachable, and nothing else breaks
	f.VPS.BlockLogins("proxier", true)
	f.selfcheck()
	if l := latestSelf(t, f); l.Class != health.SelfUnreachable {
		t.Fatalf("result %+v", l)
	}
}

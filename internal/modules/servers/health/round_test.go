package health_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/health"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func (f *fixture) countJobs(typ string) int {
	f.T.Helper()
	var n int
	if err := f.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE type = ?`, typ); err != nil {
		f.T.Fatal(err)
	}
	return n
}

func (f *fixture) countResults(kind string) int {
	f.T.Helper()
	var n int
	if err := f.App.DB.R.Get(&n, `SELECT count(*) FROM servers_check_results WHERE kind = ? AND server_id = ?`, kind, f.ID); err != nil {
		f.T.Fatal(err)
	}
	return n
}

// waitJob waits for the newest job of a type to end.
func (f *fixture) waitJob(typ string) jobs.Job {
	f.T.Helper()
	var j jobs.Job
	f.WaitFor("a "+typ+" job to end", func() bool {
		id := f.LastJob(typ)
		if id == 0 {
			return false
		}
		var err error
		if j, err = f.App.Jobs.Job(bg, id); err != nil {
			f.T.Fatal(err)
		}
		return j.State == jobs.Succeeded || j.State == jobs.Failed
	})
	return j
}

// holdRestart starts a restart on the server and stops it in the middle: the
// server is busy with a mutating job until release is called.
func (f *fixture) holdRestart() (release func()) {
	f.T.Helper()
	reached, release := f.VPS.Hold(remote.OpComposeRestart)
	if _, err := f.Mod.Deploy.Restart(bg, f.ID, "admin"); err != nil {
		f.T.Fatal(err)
	}
	f.WaitFor("the restart to reach the server", func() bool {
		select {
		case <-reached:
			return true
		default:
			return false
		}
	})
	return release
}

func TestRoundSkipsBusyServer(t *testing.T) {
	f := newFixture(t)
	f.Healthy()
	before := f.Health()
	release := f.holdRestart()

	if _, err := f.App.Jobs.EnqueueNow(bg, jobsRequest(health.JobRound)); err != nil {
		t.Fatal(err)
	}
	if j := f.waitJob(health.JobRound); j.State != jobs.Succeeded {
		t.Fatalf("round %s: %s", j.State, j.Error)
	}
	if n := f.countJobs(health.JobSelfcheck) + f.countJobs(health.JobProxytest); n != 0 {
		t.Fatalf("%d check jobs were queued for a server a redeploy is busy with", n)
	}
	release()
	f.Drain()
	after := f.Health()
	if after.Health != before.Health || after.CandidateCount != before.CandidateCount || len(f.Changes()) != 1 {
		t.Errorf("the state moved: %+v → %+v", before, after)
	}
}

func TestRoundRunsBothChecks(t *testing.T) {
	f := newFixture(t)
	f.Svc.ProxyDelay = 150 * time.Second

	if _, err := f.App.Jobs.EnqueueNow(bg, jobsRequest(health.JobRound)); err != nil {
		t.Fatal(err)
	}
	f.WaitFor("the self-check", func() bool {
		return f.countResults("self") == 1
	})
	// the queued self-check does not make the proxy test skip: it waits its 150 s
	f.WaitFor("the proxy test to be queued", func() bool { return f.countJobs(health.JobProxytest) == 1 })
	time.Sleep(50 * time.Millisecond)
	if f.countResults("proxy") != 0 {
		t.Fatal("the proxy test ran before its delay")
	}
	f.Tick(150 * time.Second)
	f.WaitFor("the proxy test", func() bool { return f.countResults("proxy") == 1 })
	f.Drain()
	if h := f.Health(); h.Health != health.Healthy {
		t.Errorf("an idle, working server is %s (%s)", h.Health, h.Reason)
	}
	// a redeploy running when the proxy test starts: it stores nothing
	release := f.holdRestart()
	f.Tick(time.Minute)
	if err := f.queueProxytest(); err != nil {
		t.Fatal(err)
	}
	before := f.countResults("proxy")
	f.WaitFor("the proxy test to end", func() bool {
		j, _ := f.App.Jobs.Job(bg, f.LastJob(health.JobProxytest))
		return j.State == jobs.Succeeded
	})
	if f.countResults("proxy") != before {
		t.Errorf("a proxy test stored a result while a job held the server")
	}
	release()
	f.Drain()
}

func (f *fixture) queueProxytest() error {
	_, err := f.App.Jobs.EnqueueNow(bg, jobs.Request{
		Type: health.JobProxytest, Subject: store.ServerSubject(f.ID), CreatedBy: "test", Payload: map[string]any{"server_id": f.ID},
	})
	return err
}

func TestOnlyActiveServersAreChecked(t *testing.T) {
	f := newFixture(t)
	var ids []int64
	for i, st := range []string{"provisioning", "failed", "retired"} {
		res, err := f.App.DB.W.Exec(`INSERT INTO servers_servers (location_id, number, name, ip, management_hostname, proxy_hostname, state,
			template_id, template_version, created_at) VALUES (?, ?, ?, ?, 'h', 'h', ?, ?, 1, '2026-10-07T00:00:00.000Z')`,
			f.LocationID, 20+i, "x-"+st, "203.0.113.2"+string(rune('0'+i)), st, f.TemplateID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	if got, err := store.CheckableServerIDs(bg, f.App.DB.R); err != nil || len(got) != 1 || got[0] != f.ID {
		t.Fatalf("checkable servers %v %v", got, err)
	}
	if _, err := f.App.Jobs.EnqueueNow(bg, jobsRequest(health.JobRound)); err != nil {
		t.Fatal(err)
	}
	f.waitJob(health.JobRound)
	var subjects []string
	if err := f.App.DB.R.Select(&subjects, `SELECT DISTINCT subject_id FROM jobs WHERE type IN (?, ?)`, health.JobSelfcheck, health.JobProxytest); err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 1 || subjects[0] != sid(f.ID) {
		t.Fatalf("checks were queued for %v", subjects)
	}
	for _, id := range ids {
		if err := f.Svc.Evaluate(bg, id, "test"); err != nil {
			t.Fatal(err)
		}
		if h, err := store.GetHealth(bg, f.App.DB.R, id); err != nil || h.Health != "" {
			t.Errorf("server %d has health %q", id, h.Health)
		}
	}
	if n := len(f.Events("server.health_changed")); n != 0 {
		t.Errorf("%d health events", n)
	}
}

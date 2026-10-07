package deploy_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

var bg = context.Background()

// stubbed is a harness whose proxy tests are stubs: nothing here pushes real
// XHTTP traffic unless a test asks for it.
func stubbed(t *testing.T) (*serverstest.Harness, int64) {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	if s := h.Server(id); s.State != "active" {
		t.Fatalf("server %s: %s %s", s.State, s.FailedStep, s.FailedError)
	}
	return h, id
}

// addParam publishes a version that declares one more parameter (not used by
// any file, so only the manifest changes).
func addParam(key string, required bool, extra string) func(map[string][]byte) {
	return func(files map[string][]byte) {
		req := "false"
		if required {
			req = "true"
		}
		files["manifest.yaml"] = []byte(strings.Replace(string(files["manifest.yaml"]), "generated:",
			"  - key: "+key+"\n    label: "+key+"\n    type: string\n    required: "+req+"\n    sample: sample-value\n"+extra+"\ngenerated:", 1))
	}
}

// change replaces text in a file of the template.
func change(path, from, to string) func(map[string][]byte) {
	return func(files map[string][]byte) {
		if !strings.Contains(string(files[path]), from) {
			panic("the seed template has no " + from + " in " + path)
		}
		files[path] = []byte(strings.Replace(string(files[path]), from, to, 1))
	}
}

// all runs every change in order.
func all(fs ...func(map[string][]byte)) func(map[string][]byte) {
	return func(files map[string][]byte) {
		for _, f := range fs {
			f(files)
		}
	}
}

// apply plans the target, applies it with the plan's hash and waits for the
// jobs. It returns the job's id.
func apply(t *testing.T, h *serverstest.Harness, id int64, target deploy.Target) int64 {
	t.Helper()
	plan, err := h.Mod.Deploy.Plan(bg, id, target)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	job, err := h.Mod.Deploy.Apply(bg, id, target, plan.Hash, "admin")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	h.Drain()
	return job
}

func mustState(t *testing.T, h *serverstest.Harness, job int64, want jobs.State) jobs.Job {
	t.Helper()
	j, err := h.App.Jobs.Job(bg, job)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != want {
		t.Fatalf("job %d is %s (%s at %s), want %s\n%s", job, j.State, j.Error, j.ErrorStep, want, h.Log(job))
	}
	return j
}

func deployments(t *testing.T, h *serverstest.Harness, id int64) []store.Deployment {
	t.Helper()
	d, err := store.Deployments(bg, h.App.DB.R, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func latest(t *testing.T, h *serverstest.Harness, id int64) store.Deployment {
	t.Helper()
	d, ok, err := store.LatestDeployment(bg, h.App.DB.R, id)
	if err != nil || !ok {
		t.Fatalf("latest deployment: %v %v", ok, err)
	}
	return d
}

func jobCount(t *testing.T, h *serverstest.Harness, typ string) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE type = ?`, typ); err != nil {
		t.Fatal(err)
	}
	return n
}

type sqlxTx = sqlx.Tx

func dbNow() db.Time { return db.Now() }

// watchOverlap polls the job table and fails the test if it ever sees more than
// one job running that matches where (a SQL condition on the jobs table). The
// returned function stops the watch. A job's started_at is taken before it waits
// for the write connection, so timestamps can't tell: the states can.
func watchOverlap(t *testing.T, h *serverstest.Harness, where string, args ...any) (stop func()) {
	t.Helper()
	done := make(chan struct{})
	finished := make(chan struct{})
	var overlapped atomic.Bool
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
			}
			var n int
			if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE state = 'running' AND `+where, args...); err == nil && n > 1 {
				overlapped.Store(true)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return func() {
		close(done)
		<-finished
		if overlapped.Load() {
			t.Errorf("two jobs matching %q ran at the same time", where)
		}
	}
}

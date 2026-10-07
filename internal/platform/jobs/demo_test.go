package jobs_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
)

func demoHarness(t *testing.T) *jobstest.Harness {
	h := newH(t)
	types, _ := h.Sys.PlatformTypes()
	if err := h.Sys.Register("platform", types...); err != nil {
		t.Fatal(err)
	}
	h.Start(h.Sys)
	return h
}

func TestDemoJob(t *testing.T) {
	t.Run("counts", func(t *testing.T) {
		h := demoHarness(t)
		e := enq(t, h, jobs.Request{Type: "platform.demo", Payload: jobs.DemoPayload{Seconds: 3, IntervalMS: 5}})
		h.Drain()
		if st := h.State(e.ID); st != jobs.Succeeded {
			t.Fatalf("state %s", st)
		}
		if l := logs(t, h, e.ID); !contains(l, "Counting: 3 of 3") || !contains(l, "Done") {
			t.Fatalf("%q", l)
		}
	})
	t.Run("cancels mid-count", func(t *testing.T) {
		h := demoHarness(t)
		e := enq(t, h, jobs.Request{Type: "platform.demo", Payload: jobs.DemoPayload{Seconds: 100000, IntervalMS: 5}})
		h.WaitFor("a few lines", func() bool {
			return count(t, h, `SELECT count(*) FROM job_log_lines WHERE job_id = ? AND text LIKE 'Counting%'`, e.ID) >= 3
		})
		if err := h.Sys.Cancel(bg, e.ID, "admin"); err != nil {
			t.Fatal(err)
		}
		h.Drain()
		if st := h.State(e.ID); st != jobs.Cancelled {
			t.Fatalf("state %s", st)
		}
	})
	t.Run("fails when asked", func(t *testing.T) {
		h := demoHarness(t)
		e := enq(t, h, jobs.Request{Type: "platform.demo", Payload: jobs.DemoPayload{Seconds: 1, IntervalMS: 5, Fail: true}})
		h.Drain()
		j, _ := h.Sys.Job(bg, e.ID)
		if j.State != jobs.Failed || j.Error != "demo failure, as asked" || j.ErrorStep != "finish" {
			t.Fatalf("%+v", j)
		}
		if n := count(t, h, `SELECT count(*) FROM events WHERE type = 'job.failed'`); n != 1 {
			t.Fatalf("%d job.failed events", n)
		}
	})
}

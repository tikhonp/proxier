package jobs_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
)

var bg = context.Background()

func newH(t *testing.T) *jobstest.Harness { t.Helper(); return jobstest.New(t) }

func reg(t *testing.T, s *jobs.System, types ...jobs.Type) {
	t.Helper()
	if err := s.Register("test", types...); err != nil {
		t.Fatal(err)
	}
}

// simple is a one-step type on the checks queue.
func simple(name string, run func(ctx context.Context, r *jobs.Run) error) jobs.Type {
	return jobs.Type{Name: name, Queue: jobs.Checks, MaxAttempts: 1, Steps: []jobs.Step{{Name: "run", Run: run}}}
}

func enq(t *testing.T, h *jobstest.Harness, r jobs.Request) jobs.Enqueued {
	t.Helper()
	if r.CreatedBy == "" {
		r.CreatedBy = "admin"
	}
	e, err := h.Sys.EnqueueNow(bg, r)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return e
}

func count(t *testing.T, h *jobstest.Harness, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.DB.R.Get(&n, query, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

func logs(t *testing.T, h *jobstest.Harness, id int64) []string {
	t.Helper()
	var out []string
	if err := h.DB.R.Select(&out, `SELECT text FROM job_log_lines WHERE job_id = ? ORDER BY id`, id); err != nil {
		t.Fatal(err)
	}
	return out
}

func contains(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}

// gate blocks a step until released, or the context ends.
type gate struct {
	reached atomic.Int32
	ch      chan struct{}
}

func newGate() *gate { return &gate{ch: make(chan struct{})} }

func (g *gate) wait(ctx context.Context) error {
	g.reached.Add(1)
	select {
	case <-g.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gate) release() { close(g.ch) }

func waitInt(t *testing.T, h *jobstest.Harness, what string, f func() int32, want int32) {
	t.Helper()
	h.WaitFor(what, func() bool { return f() >= want })
}

var _ = time.Second

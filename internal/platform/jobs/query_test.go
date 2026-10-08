package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func TestBusyMeansRunning(t *testing.T) {
	h := newH(t)
	g := newGate()
	reg(t, h.Sys, simple("test.hold", func(ctx context.Context, r *jobs.Run) error { return g.wait(ctx) }))
	h.Start(h.Sys)

	busy := func() bool {
		t.Helper()
		b, err := h.Sys.Busy(bg, "server:7")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if busy() {
		t.Fatal("busy with no job")
	}
	// Queued (delayed) does not count.
	enq(t, h, jobs.Request{Type: "test.hold", ResourceKey: "server:7", Delay: time.Hour})
	if busy() {
		t.Fatal("a queued job made the key busy")
	}
	// Running does; another key does not.
	enq(t, h, jobs.Request{Type: "test.hold", ResourceKey: "server:7"})
	waitInt(t, h, "job running", g.reached.Load, 1)
	if !busy() {
		t.Fatal("a running job did not make the key busy")
	}
	if b, _ := h.Sys.Busy(bg, "server:8"); b {
		t.Fatal("another key is busy")
	}
	g.release()
	h.WaitFor("key free", func() bool { return !busy() })
}

func TestCancelQueuedCancelsOnlyUnstartedJobsOfTheSubject(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.resume", nop))
	a := enq(t, h, jobs.Request{Type: "test.resume", Subject: events.Subject{Type: "server", ID: "1"}, Delay: time.Hour})
	b := enq(t, h, jobs.Request{Type: "test.resume", Subject: events.Subject{Type: "server", ID: "2"}, Delay: time.Hour})
	var n int
	err := h.DB.Write(bg, func(tx *sqlx.Tx) (err error) {
		n, err = h.Sys.CancelQueued(bg, tx, "test.resume", events.Subject{Type: "server", ID: "1"}, "admin")
		return err
	})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	ja, _ := h.Sys.Job(bg, a.ID)
	jb, _ := h.Sys.Job(bg, b.ID)
	if ja.State != jobs.Cancelled || jb.State != jobs.Queued {
		t.Fatalf("states %s %s", ja.State, jb.State)
	}
}

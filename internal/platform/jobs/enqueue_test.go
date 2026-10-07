package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func nop(context.Context, *jobs.Run) error { return nil }

func TestCoalescingMergesIntoQueuedJob(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.sync", nop))
	a := enq(t, h, jobs.Request{Type: "test.sync", Payload: map[string]any{"a": 1}, CoalescingKey: "3", Delay: 30 * time.Second})
	b := enq(t, h, jobs.Request{Type: "test.sync", Payload: map[string]any{"b": 2}, CoalescingKey: "3", Delay: 30 * time.Second})
	if a.Merged || !b.Merged || a.ID != b.ID {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	j, err := h.Sys.Job(bg, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]int
	_ = json.Unmarshal(j.Payload, &p)
	if p["a"] != 1 || p["b"] != 2 || j.Merged != 1 {
		t.Fatalf("payload %s merged %d", j.Payload, j.Merged)
	}
	if n := count(t, h, `SELECT count(*) FROM jobs`); n != 1 {
		t.Fatalf("%d jobs", n)
	}
}

func TestCoalescingQueuesOneFollowUp(t *testing.T) {
	h := newH(t)
	g := newGate()
	reg(t, h.Sys, simple("test.sync", func(ctx context.Context, r *jobs.Run) error { return g.wait(ctx) }))
	h.Start(h.Sys)
	first := enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "3", ResourceKey: "router:3"})
	waitInt(t, h, "first job running", g.reached.Load, 1)

	second := enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "3", ResourceKey: "router:3"})
	third := enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "3", ResourceKey: "router:3"})
	if second.ID == first.ID || second.Merged {
		t.Fatalf("expected a follow-up, got %+v", second)
	}
	if third.ID != second.ID || !third.Merged {
		t.Fatalf("third should merge into the follow-up: %+v", third)
	}
	g.release()
	h.Drain()
	if n := count(t, h, `SELECT count(*) FROM jobs`); n != 2 {
		t.Fatalf("%d jobs", n)
	}
}

func TestSkipIfBusy(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.redeploy", nop), simple("test.check", nop))
	enq(t, h, jobs.Request{Type: "test.redeploy", ResourceKey: "server:12"})
	_, err := h.Sys.EnqueueNow(bg, jobs.Request{Type: "test.check", ResourceKey: "server:12", SkipIfBusy: true, CreatedBy: "system"})
	if !errors.Is(err, jobs.ErrSkipped) {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, h, `SELECT count(*) FROM jobs`); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	// A different resource is not busy.
	if _, err := h.Sys.EnqueueNow(bg, jobs.Request{Type: "test.check", ResourceKey: "server:13", SkipIfBusy: true, CreatedBy: "system"}); err != nil {
		t.Fatal(err)
	}
}

func TestCoalescingSkipsJobsThatStarted(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, jobs.Type{Name: "test.sync", Queue: jobs.Checks, MaxAttempts: 2, Backoff: []time.Duration{time.Hour},
		Steps: []jobs.Step{{Name: "run", Run: func(context.Context, *jobs.Run) error { return errors.New("boom") }}}})
	h.Start(h.Sys)
	a := enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "k"})
	h.Drain()
	if st := h.State(a.ID); st != jobs.Queued {
		t.Fatalf("state %s", st)
	}
	b := enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "k"})
	if b.Merged || b.ID == a.ID {
		t.Fatalf("merged into a job that had started: %+v", b)
	}
}

func TestMergeKeepsLaterRunAfter(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.sync", nop))
	a := enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "k", Delay: 10 * time.Second})
	enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "k", Delay: 60 * time.Second})
	enq(t, h, jobs.Request{Type: "test.sync", CoalescingKey: "k", Delay: 5 * time.Second})
	j, _ := h.Sys.Job(bg, a.ID)
	if want := h.Clock.Now().Add(60 * time.Second); !j.RunAfter.Equal(want) {
		t.Fatalf("run after %v, want %v", j.RunAfter, want)
	}
	if j.Merged != 2 {
		t.Fatalf("merged %d", j.Merged)
	}
}

func TestEnqueueCommitsWithTheChange(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.sync", nop))
	boom := errors.New("caller failed")
	err := h.DB.Write(bg, func(tx *sqlx.Tx) error {
		if _, err := h.Sys.Enqueue(bg, tx, jobs.Request{Type: "test.sync", CreatedBy: "admin"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n := count(t, h, `SELECT count(*) FROM jobs`); n != 0 {
		t.Fatalf("%d jobs survived a rollback", n)
	}
	if err := h.DB.Write(bg, func(tx *sqlx.Tx) error {
		_, err := h.Sys.Enqueue(bg, tx, jobs.Request{Type: "test.sync", CreatedBy: "admin"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, h, `SELECT count(*) FROM jobs`); n != 1 {
		t.Fatalf("%d jobs", n)
	}
}

func TestCoalescingKeyIsPerType(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.a", nop), simple("test.b", nop))
	a := enq(t, h, jobs.Request{Type: "test.a", CoalescingKey: "12", Delay: time.Minute})
	b := enq(t, h, jobs.Request{Type: "test.b", CoalescingKey: "12", Delay: time.Minute})
	if a.ID == b.ID || b.Merged {
		t.Fatalf("merged across types: %+v %+v", a, b)
	}
}

func TestPayloadTooLargeRefused(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.sync", nop))
	big := strings.Repeat("x", 70<<10)
	_, err := h.Sys.EnqueueNow(bg, jobs.Request{Type: "test.sync", Payload: map[string]string{"v": big}, CreatedBy: "admin"})
	if !errors.Is(err, jobs.ErrPayloadTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, h, `SELECT count(*) FROM jobs`); n != 0 {
		t.Fatalf("%d jobs stored", n)
	}

	// Each half fits; the merge doesn't.
	half := strings.Repeat("y", 40<<10)
	a := enq(t, h, jobs.Request{Type: "test.sync", Payload: map[string]string{"a": half}, CoalescingKey: "k", Delay: time.Minute})
	_, err = h.Sys.EnqueueNow(bg, jobs.Request{Type: "test.sync", Payload: map[string]string{"b": half}, CoalescingKey: "k",
		Delay: time.Minute, CreatedBy: "admin"})
	if !errors.Is(err, jobs.ErrPayloadTooLarge) {
		t.Fatalf("merge err = %v", err)
	}
	j, _ := h.Sys.Job(bg, a.ID)
	if j.Merged != 0 || strings.Contains(string(j.Payload), `"b"`) {
		t.Fatal("the queued job changed")
	}
}

func TestEnqueueRefusesUnknownTypeAndMissingActor(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.sync", nop))
	if _, err := h.Sys.EnqueueNow(bg, jobs.Request{Type: "test.nope", CreatedBy: "admin"}); !errors.Is(err, jobs.ErrUnknownType) {
		t.Fatalf("err = %v", err)
	}
	if _, err := h.Sys.EnqueueNow(bg, jobs.Request{Type: "test.sync"}); err == nil {
		t.Fatal("a request without CreatedBy was accepted")
	}
}

var _ = db.Now

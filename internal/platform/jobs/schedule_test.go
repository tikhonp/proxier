package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
)

func scheduled(t *testing.T, h *jobstest.Harness, c jobs.Schedule) {
	t.Helper()
	reg(t, h.Sys, simple("test.round", nop))
	if c.Name == "" {
		c.Name = "test.round"
	}
	c.Request = func(context.Context) (jobs.Request, error) { return jobs.Request{Type: "test.round"}, nil }
	if err := h.Sys.RegisterSchedules("test", c); err != nil {
		t.Fatal(err)
	}
}

func jobCount(t *testing.T, h *jobstest.Harness) int {
	return count(t, h, `SELECT count(*) FROM jobs`)
}

func TestScheduleNeverOverlapsItself(t *testing.T) {
	h := newH(t)
	scheduled(t, h, jobs.Schedule{Every: time.Hour})
	run := func() {
		t.Helper()
		if err := h.Sys.RunSchedules(bg); err != nil {
			t.Fatal(err)
		}
	}
	run() // first sight: the rhythm starts
	h.Clock.Advance(time.Hour + time.Minute)
	run() // due: enqueues
	if n := jobCount(t, h); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	h.Clock.Advance(time.Hour) // the job is still queued (no pool runs)
	run()
	if n := jobCount(t, h); n != 1 {
		t.Fatalf("%d jobs: the schedule overlapped itself", n)
	}
	var s struct {
		Skipped int     `db:"skipped"`
		At      *string `db:"last_skipped_at"`
	}
	if err := h.DB.R.Get(&s, `SELECT skipped, last_skipped_at FROM schedules WHERE name = 'test.round'`); err != nil {
		t.Fatal(err)
	}
	if s.Skipped != 1 || s.At == nil {
		t.Fatalf("%+v", s)
	}
}

func TestScheduleNextRun(t *testing.T) {
	h := newH(t)
	set := func(tz string) {
		t.Helper()
		if err := h.Settings.Set(bg, "admin", "general", map[string]string{"general.time_zone": tz}); err != nil {
			t.Fatal(err)
		}
	}
	h.Sys.SetJitter(func(max time.Duration) time.Duration { return max / 2 })
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	if got := h.Sys.NextRun(jobs.Schedule{Every: 5 * time.Minute}, now); !got.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("interval: %v", got)
	}
	if got := h.Sys.NextRun(jobs.Schedule{Every: 5 * time.Minute, Jitter: 20 * time.Second}, now); !got.Equal(now.Add(5*time.Minute + 10*time.Second)) {
		t.Fatalf("interval with jitter: %v", got)
	}

	// Moscow has no DST: 03:30 is 00:30 UTC.
	set("Europe/Moscow")
	if got := h.Sys.NextRun(jobs.Schedule{At: "03:30"}, now); !got.Equal(time.Date(2026, 3, 11, 0, 30, 0, 0, time.UTC)) {
		t.Fatalf("Moscow: %v", got)
	}
	// Not yet reached today: today.
	early := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC) // 23:00 in Moscow
	if got := h.Sys.NextRun(jobs.Schedule{At: "23:30"}, early); !got.Equal(time.Date(2026, 3, 10, 20, 30, 0, 0, time.UTC)) {
		t.Fatalf("later today: %v", got)
	}

	// Berlin switches to summer time on 2026-03-29: 03:30 that day is 01:30 UTC, not 02:30.
	set("Europe/Berlin")
	before := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	if got := h.Sys.NextRun(jobs.Schedule{At: "03:30"}, before); !got.Equal(time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("Berlin across DST: %v", got)
	}
}

func TestMissedScheduleRunsOnce(t *testing.T) {
	h := newH(t)
	scheduled(t, h, jobs.Schedule{Every: time.Hour})
	run := func() {
		t.Helper()
		if err := h.Sys.RunSchedules(bg); err != nil {
			t.Fatal(err)
		}
	}
	run()
	h.Clock.Advance(10 * time.Hour) // downtime: ten rounds missed
	run()
	run()
	if n := jobCount(t, h); n != 1 {
		t.Fatalf("%d jobs after downtime, want one", n)
	}
	var next string
	_ = h.DB.R.Get(&next, `SELECT next_run_at FROM schedules WHERE name = 'test.round'`)
	if want := h.Clock.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"); next != want {
		t.Fatalf("next %s, want %s", next, want)
	}
}

func TestScheduleFollowsSetting(t *testing.T) {
	h := newH(t)
	scheduled(t, h, jobs.Schedule{Every: time.Hour, Setting: "test.every"})
	if err := h.Settings.Set(bg, "admin", "test", map[string]string{"test.every": "10m"}); err != nil {
		t.Fatal(err)
	}
	_ = h.Sys.RunSchedules(bg)
	h.Clock.Advance(11 * time.Minute)
	_ = h.Sys.RunSchedules(bg)
	if n := jobCount(t, h); n != 1 {
		t.Fatalf("%d jobs; the setting's 10 m should have applied", n)
	}
}

func TestDisabledScheduleDoesNotRun(t *testing.T) {
	h := newH(t)
	scheduled(t, h, jobs.Schedule{Every: time.Hour})
	_ = h.Sys.RunSchedules(bg)
	if err := h.Sys.SetScheduleEnabled(bg, "test.round", false, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(5 * time.Hour)
	_ = h.Sys.RunSchedules(bg)
	h.Clock.Advance(5 * time.Hour)
	_ = h.Sys.RunSchedules(bg)
	if n := jobCount(t, h); n != 0 {
		t.Fatalf("%d jobs from a disabled schedule", n)
	}
	if n := count(t, h, `SELECT skipped FROM schedules WHERE name = 'test.round'`); n != 0 {
		t.Fatalf("%d skips counted", n)
	}
	if err := h.Sys.SetScheduleEnabled(bg, "test.round", true, "admin"); err != nil {
		t.Fatal(err)
	}
	_ = h.Sys.RunSchedules(bg) // no catch-up for the ten hours
	if n := jobCount(t, h); n != 0 {
		t.Fatalf("%d jobs: enabling must not catch up", n)
	}
	h.Clock.Advance(61 * time.Minute)
	_ = h.Sys.RunSchedules(bg)
	if n := jobCount(t, h); n != 1 {
		t.Fatalf("%d jobs after one rhythm", n)
	}
}

func TestSetScheduleEnabledRecordsEvent(t *testing.T) {
	h := newH(t)
	scheduled(t, h, jobs.Schedule{Every: time.Hour})
	events := func() int { return count(t, h, `SELECT count(*) FROM events WHERE type = 'schedule.enabled_changed'`) }
	for _, step := range []struct {
		enabled bool
		want    int
	}{{true, 0}, {false, 1}, {false, 1}, {true, 2}} {
		if err := h.Sys.SetScheduleEnabled(bg, "test.round", step.enabled, "admin"); err != nil {
			t.Fatal(err)
		}
		if got := events(); got != step.want {
			t.Fatalf("after enabled=%v: %d events, want %d", step.enabled, got, step.want)
		}
	}
	if err := h.Sys.SetScheduleEnabled(bg, "test.nope", true, "admin"); err == nil {
		t.Fatal("unknown schedule accepted")
	}
}

func TestSchedulerLoopEnqueuesDueJobs(t *testing.T) {
	h := newH(t)
	scheduled(t, h, jobs.Schedule{Every: time.Hour})
	h.Start(h.Sys)
	h.WaitFor("schedule row", func() bool { return count(t, h, `SELECT count(*) FROM schedules`) == 1 })
	h.Clock.Advance(2 * time.Hour)
	h.WaitFor("the scheduled job to run", func() bool {
		return count(t, h, `SELECT count(*) FROM jobs WHERE state = 'succeeded' AND created_by = 'schedule:test.round'`) == 1
	})
}

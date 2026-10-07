package jobs_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
)

func ago(h *jobstest.Harness, d time.Duration) db.Time { return db.At(h.Clock.Now().Add(-d)) }

// finished makes a job of type typ in state, finished d ago, and returns its id.
func finished(t *testing.T, h *jobstest.Harness, typ string, state string, d time.Duration, attempt int) int64 {
	t.Helper()
	e := enq(t, h, jobs.Request{Type: typ})
	if _, err := h.DB.W.Exec(`UPDATE jobs SET state = ?, finished_at = ?, attempt = ? WHERE id = ?`, state, ago(h, d), attempt, e.ID); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func exists(t *testing.T, h *jobstest.Harness, id int64) bool {
	return count(t, h, `SELECT count(*) FROM jobs WHERE id = ?`, id) == 1
}

const day = 24 * time.Hour

func TestRetentionKeepsFailedJobsLonger(t *testing.T) {
	h := newH(t)
	reg(t, h.Sys, simple("test.sync", nop))
	failed31 := finished(t, h, "test.sync", "failed", 31*day, 1)
	ok31 := finished(t, h, "test.sync", "succeeded", 31*day, 1)
	failed91 := finished(t, h, "test.sync", "failed", 91*day, 1)
	ok29 := finished(t, h, "test.sync", "succeeded", 29*day, 1)
	// Lines and steps go with their job.
	if _, err := h.DB.W.Exec(`INSERT INTO job_log_lines (job_id, seq, time, level, attempt, text) VALUES (?, 1, ?, 'info', 1, 'x')`, ok31, ago(h, 31*day)); err != nil {
		t.Fatal(err)
	}
	n, err := h.Sys.PruneJobs(bg)
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v", n, err)
	}
	if !exists(t, h, failed31) || exists(t, h, ok31) || exists(t, h, failed91) || !exists(t, h, ok29) {
		t.Fatal("wrong jobs removed")
	}
	if c := count(t, h, `SELECT count(*) FROM job_log_lines WHERE job_id = ?`, ok31); c != 0 {
		t.Fatal("log lines outlived their job")
	}
	if c := count(t, h, `SELECT count(*) FROM job_steps WHERE job_id = ?`, ok31); c != 0 {
		t.Fatal("steps outlived their job")
	}
}

func TestRetentionDeletesCleanQuietJobsAfterADay(t *testing.T) {
	h := newH(t)
	q := simple("test.check", nop)
	q.Quiet = true
	reg(t, h.Sys, q, simple("test.sync", nop))
	clean := finished(t, h, "test.check", "succeeded", 25*day/24, 1)
	fresh := finished(t, h, "test.check", "succeeded", 23*day/24, 1)
	failed := finished(t, h, "test.check", "failed", 25*day/24, 1)
	retried := finished(t, h, "test.check", "succeeded", 25*day/24, 2)
	cancelled := finished(t, h, "test.check", "cancelled", 25*day/24, 0)
	loud := finished(t, h, "test.sync", "succeeded", 25*day/24, 1)
	if _, err := h.Sys.PruneJobs(bg); err != nil {
		t.Fatal(err)
	}
	if exists(t, h, clean) {
		t.Fatal("a clean quiet job outlived a day")
	}
	for name, id := range map[string]int64{"fresh": fresh, "failed": failed, "retried": retried, "cancelled": cancelled, "loud": loud} {
		if !exists(t, h, id) {
			t.Fatalf("%s job was removed", name)
		}
	}
}

func TestRetentionPrunesSignInData(t *testing.T) {
	h := newH(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := h.DB.W.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at) VALUES (1, 'a', 'x', 'en', ?, ?)`, ago(h, 90*day), ago(h, 90*day))
	exec(`INSERT INTO sign_in_attempts (ip, time, success) VALUES ('1.1.1.1', ?, 0), ('1.1.1.1', ?, 1)`, ago(h, 31*day), ago(h, day))
	sess := func(token string, expires, ended any, reason any) {
		exec(`INSERT INTO sessions (token_hash, admin_id, created_at, last_seen_at, expires_at, created_ip, last_ip, ended_at, end_reason)
			VALUES (CAST(? AS BLOB), 1, ?, ?, ?, 'ip', 'ip', ?, ?)`, token, ago(h, 60*day), ago(h, 60*day), expires, ended, reason)
	}
	future := db.At(h.Clock.Now().Add(10 * day))
	sess("ended-old", future, ago(h, 31*day), "signed_out")
	sess("ended-recent", future, ago(h, 2*day), "signed_out")
	sess("expired-old", ago(h, 31*day), nil, nil)
	sess("open", future, nil, nil)
	a, s, err := h.Sys.PruneSignIn(bg)
	if err != nil || a != 1 || s != 2 {
		t.Fatalf("attempts %d sessions %d err %v", a, s, err)
	}
	if c := count(t, h, `SELECT count(*) FROM sessions`); c != 2 {
		t.Fatalf("%d sessions left", c)
	}
}

func TestRetentionJobRuns(t *testing.T) {
	h := newH(t)
	types, schedules := h.Sys.PlatformTypes()
	if err := h.Sys.Register("platform", types...); err != nil {
		t.Fatal(err)
	}
	if err := h.Sys.RegisterSchedules("platform", schedules...); err != nil {
		t.Fatal(err)
	}
	h.Start(h.Sys)
	e := enq(t, h, jobs.Request{Type: "platform.retention"})
	h.Drain()
	if st := h.State(e.ID); st != jobs.Succeeded {
		t.Fatalf("state %s", st)
	}
}

func TestRetentionKeepsNotificationsNinetyDays(t *testing.T) {
	h := newH(t)
	add := func(event int, state string, d time.Duration) {
		t.Helper()
		_, err := h.DB.W.Exec(`INSERT INTO notifications (event_id, channel, lang, message, state, created_at) VALUES (?, 'telegram', 'en', '{}', ?, ?)`,
			event, state, ago(h, d))
		if err != nil {
			t.Fatal(err)
		}
	}
	add(1, "sent", 91*day)
	add(2, "failed", 91*day)
	add(3, "queued", 91*day)
	add(4, "sent", 89*day)
	add(5, "failed", 2*day)
	n, err := h.Sys.PruneNotifications(bg)
	if err != nil || n != 3 {
		t.Fatalf("removed %d: %v", n, err)
	}
	if c := count(t, h, `SELECT count(*) FROM notifications`); c != 2 {
		t.Fatalf("%d left", c)
	}
}

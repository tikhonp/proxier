package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

const now = "2026-10-07T00:00:00.000Z"

func exec(t *testing.T, d *db.DB, q string, args ...any) error {
	t.Helper()
	_, err := d.W.ExecContext(context.Background(), q, args...)
	return err
}

func mustExec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if err := exec(t, d, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func mustFail(t *testing.T, d *db.DB, what, q string, args ...any) {
	t.Helper()
	if err := exec(t, d, q, args...); err == nil {
		t.Fatalf("%s: accepted, want a constraint error", what)
	}
}

func TestPlatformMigrationsGoDownAndUpAgain(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	for i := 0; i < 5; i++ {
		if err := db.MigrateDown(ctx, d, dbtest.Discard, dbtest.Platform); err != nil {
			t.Fatalf("down %d: %v", i, err)
		}
	}
	if err := db.MigrateUp(ctx, d, dbtest.Discard, dbtest.Platform); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestAuthTablesRefuseWhatTheContractForbids(t *testing.T) {
	d := dbtest.Open(t)
	mustExec(t, d, `INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at) VALUES (1, 'a', 'h', 'en', ?, ?)`, now, now)
	mustFail(t, d, "second admin", `INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at) VALUES (2, 'b', 'h', 'en', ?, ?)`, now, now)
	mustFail(t, d, "unknown language", `UPDATE admin SET language = 'de'`)

	ins := `INSERT INTO sessions (token_hash, admin_id, created_at, last_seen_at, expires_at, created_ip, last_ip, ended_at, end_reason) VALUES (?, 1, ?, ?, ?, 'ip', 'ip', ?, ?)`
	mustExec(t, d, ins, []byte("t1"), now, now, now, nil, nil)
	mustFail(t, d, "duplicate token hash", ins, []byte("t1"), now, now, now, nil, nil)
	mustFail(t, d, "ended without a reason", ins, []byte("t2"), now, now, now, now, nil)
	mustFail(t, d, "a reason without ending", ins, []byte("t3"), now, now, now, nil, "signed_out")
	mustFail(t, d, "unknown reason", ins, []byte("t4"), now, now, now, now, "expired")

	mustExec(t, d, `DELETE FROM admin`)
	var n int
	if err := d.R.GetContext(context.Background(), &n, `SELECT count(*) FROM sessions`); err != nil || n != 0 {
		t.Fatalf("sessions after deleting the admin: %d, %v", n, err)
	}
	mustFail(t, d, "attempt without success flag value", `INSERT INTO sign_in_attempts (ip, time, success) VALUES ('ip', ?, 2)`, now)
}

func TestJobsTablesCoalesceAndCascade(t *testing.T) {
	d := dbtest.Open(t)
	job := `INSERT INTO jobs (queue, type, coalescing_key, state, attempt, max_attempts, run_after, created_by, created_at) VALUES ('checks', ?, ?, ?, ?, 1, ?, 'system', ?)`

	mustExec(t, d, job, "servers.check", "12", "queued", 0, now, now)
	mustFail(t, d, "two queued never-started jobs with one key and type", job, "servers.check", "12", "queued", 0, now, now)
	mustExec(t, d, job, "servers.redeploy", "12", "queued", 0, now, now) // another type: the key is per type
	mustExec(t, d, job, "servers.check", "12", "running", 1, now, now)   // a started one doesn't count
	mustExec(t, d, job, "servers.check", "", "queued", 0, now, now)      // no key, no coalescing
	mustExec(t, d, job, "servers.check", "", "queued", 0, now, now)

	mustFail(t, d, "payload over 64 KiB", `INSERT INTO jobs (queue, type, state, max_attempts, run_after, created_by, created_at, payload) VALUES ('checks', 'x', 'queued', 1, ?, 's', ?, ?)`, now, now, strings.Repeat("a", 65537))
	mustFail(t, d, "unknown state", job, "x", "", "paused", 0, now, now)
	mustFail(t, d, "quiet other than 0/1", `UPDATE jobs SET quiet = 2`)

	mustExec(t, d, `INSERT INTO job_steps (job_id, idx, name, state) VALUES (1, 0, 'a', 'pending')`)
	mustFail(t, d, "duplicate step name", `INSERT INTO job_steps (job_id, idx, name, state) VALUES (1, 1, 'a', 'pending')`)
	line := `INSERT INTO job_log_lines (job_id, seq, time, level, attempt, text) VALUES (1, ?, ?, 'info', 1, ?)`
	mustExec(t, d, line, 1, now, "hello")
	mustFail(t, d, "duplicate seq in a job", line, 1, now, "again")
	mustFail(t, d, "line over the cap", line, 2, now, strings.Repeat("a", 4101))

	mustExec(t, d, `INSERT INTO jobs (queue, type, state, max_attempts, run_after, created_by, created_at, retry_of) VALUES ('checks', 'x', 'queued', 1, ?, 's', ?, 1)`, now, now)
	mustExec(t, d, `DELETE FROM jobs WHERE id = 1`)
	var steps, lines int
	_ = d.R.GetContext(context.Background(), &steps, `SELECT count(*) FROM job_steps`)
	_ = d.R.GetContext(context.Background(), &lines, `SELECT count(*) FROM job_log_lines`)
	if steps != 0 || lines != 0 {
		t.Fatalf("steps %d, lines %d after deleting the job, want 0", steps, lines)
	}
	var retryOf *int64
	if err := d.R.GetContext(context.Background(), &retryOf, `SELECT retry_of FROM jobs WHERE type = 'x' AND retry_of IS NULL`); err != nil {
		t.Fatalf("retry_of should be nulled when the original goes: %v", err)
	}

	mustExec(t, d, `INSERT INTO schedules (name, next_run_at) VALUES ('platform.backup', ?)`, now)
	var enabled int
	if err := d.R.GetContext(context.Background(), &enabled, `SELECT enabled FROM schedules`); err != nil || enabled != 1 {
		t.Fatalf("a new schedule is enabled by default: %d, %v", enabled, err)
	}
}

func TestNotificationTablesAreOnePerEventAndChannel(t *testing.T) {
	d := dbtest.Open(t)
	ins := `INSERT INTO notifications (event_id, channel, lang, message, state, created_at) VALUES (7, 'telegram', 'en', '{}', 'queued', ?) ON CONFLICT DO NOTHING`
	mustExec(t, d, ins, now)
	mustExec(t, d, ins, now)
	var n int
	if err := d.R.GetContext(context.Background(), &n, `SELECT count(*) FROM notifications`); err != nil || n != 1 {
		t.Fatalf("notifications for one event and channel: %d, %v", n, err)
	}
	mustFail(t, d, "unknown language", `INSERT INTO notifications (event_id, channel, lang, message, state, created_at) VALUES (8, 'telegram', 'de', '{}', 'queued', ?)`, now)
	mustFail(t, d, "unknown state", `UPDATE notifications SET state = 'lost'`)
	mustExec(t, d, `INSERT INTO notification_rules (event_type, enabled, updated_at) VALUES ('auth.signed_in', 0, ?)`, now)
	mustFail(t, d, "rule enabled not 0/1", `UPDATE notification_rules SET enabled = 2`)
}

func TestSSHTablesKeepOneIdentityAndOneRowPerAddress(t *testing.T) {
	d := dbtest.Open(t)
	id := `INSERT INTO ssh_identity (id, private_key, public_key, fingerprint, created_at) VALUES (?, x'00', 'pk', 'SHA256:x', ?)`
	mustExec(t, d, id, 1, now)
	mustFail(t, d, "second identity", id, 2, now)

	host := `INSERT INTO known_hosts (address, key_type, public_key, fingerprint, first_seen_at, accepted_at, pending_public_key, pending_fingerprint) VALUES (?, 'ssh-ed25519', x'01', 'SHA256:a', ?, ?, ?, ?)`
	mustExec(t, d, host, "1.2.3.4:22", now, now, nil, nil)
	mustFail(t, d, "duplicate address", host, "1.2.3.4:22", now, now, nil, nil)
	mustFail(t, d, "pending key without its fingerprint", host, "5.6.7.8:22", now, now, []byte{2}, nil)
	mustExec(t, d, host, "5.6.7.8:22", now, now, []byte{2}, "SHA256:b")
}

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

// down rolls the platform back n migrations.
func down(t *testing.T, d *db.DB, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := db.MigrateDown(context.Background(), d, dbtest.Discard, dbtest.Platform); err != nil {
			t.Fatalf("down %d: %v", i, err)
		}
	}
}

func up(t *testing.T, d *db.DB) {
	t.Helper()
	if err := db.MigrateUp(context.Background(), d, dbtest.Discard, dbtest.Platform); err != nil {
		t.Fatalf("up: %v", err)
	}
}

func count(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.R.GetContext(context.Background(), &n, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestPlatformMigrationsGoDownAndUpAgain(t *testing.T) {
	d := dbtest.Open(t)
	down(t, d, 9)
	up(t, d)
}

const member = `INSERT INTO members (username, password_hash, language, webauthn_user_id, created_at, created_by, password_changed_at) VALUES (?, 'h', ?, randomblob(32), ?, 'cli', ?)`

func TestAuthTablesRefuseWhatTheContractForbids(t *testing.T) {
	d := dbtest.Open(t)
	mustExec(t, d, member, "tikhon", "en", now, now)
	mustExec(t, d, member, "anna", "ru", now, now)
	mustFail(t, d, "a username taken in another case", member, "Tikhon", "en", now, now)
	mustFail(t, d, "unknown language", `UPDATE members SET language = 'de'`)
	mustFail(t, d, "empty username", member, "", "en", now, now)

	ins := `INSERT INTO sessions (token_hash, member_id, created_at, last_seen_at, expires_at, created_ip, last_ip, ended_at, end_reason) VALUES (?, ?, ?, ?, ?, 'ip', 'ip', ?, ?)`
	mustExec(t, d, ins, []byte("t1"), 1, now, now, now, nil, nil)
	mustFail(t, d, "duplicate token hash", ins, []byte("t1"), 1, now, now, now, nil, nil)
	mustFail(t, d, "a session of no member", ins, []byte("t0"), 9, now, now, now, nil, nil)
	mustFail(t, d, "ended without a reason", ins, []byte("t2"), 1, now, now, now, now, nil)
	mustFail(t, d, "a reason without ending", ins, []byte("t3"), 1, now, now, now, nil, "signed_out")
	mustFail(t, d, "unknown reason", ins, []byte("t4"), 1, now, now, now, now, "expired")
	mustExec(t, d, ins, []byte("t5"), 2, now, now, now, now, "second_factor_reset")

	mustExec(t, d, `DELETE FROM members WHERE id = 1`)
	if n := count(t, d, `SELECT count(*) FROM sessions`); n != 1 {
		t.Fatalf("sessions after deleting member 1: %d, want only member 2's", n)
	}
	mustFail(t, d, "attempt without success flag value", `INSERT INTO sign_in_attempts (ip, time, success) VALUES ('ip', ?, 2)`, now)
	mustExec(t, d, `INSERT INTO sign_in_attempts (ip, time, success, member_id) VALUES ('ip', ?, 0, NULL)`, now)
}

// IDs are never reused: an actor member:<id> in history names one member.
func TestMemberIDsAreNotReused(t *testing.T) {
	d := dbtest.Open(t)
	mustExec(t, d, member, "a", "en", now, now)
	mustExec(t, d, member, "b", "en", now, now)
	mustExec(t, d, `DELETE FROM members WHERE id = 2`)
	mustExec(t, d, member, "c", "en", now, now)
	if n := count(t, d, `SELECT id FROM members WHERE username = 'c'`); n != 3 {
		t.Fatalf("the next member got ID %d, want 3", n)
	}
}

func TestSecondFactorTablesKeepOneAppPerMemberAndUniquePasskeys(t *testing.T) {
	d := dbtest.Open(t)
	mustExec(t, d, member, "a", "en", now, now)
	mustExec(t, d, member, "b", "en", now, now)
	mustExec(t, d, `INSERT INTO totp (member_id, secret, created_at) VALUES (1, x'01', ?)`, now)
	mustFail(t, d, "second authenticator app of a member", `INSERT INTO totp (member_id, secret, created_at) VALUES (1, x'02', ?)`, now)
	mustExec(t, d, `INSERT INTO totp (member_id, secret, created_at) VALUES (2, x'02', ?)`, now)
	mustFail(t, d, "an app of no member", `INSERT INTO totp (member_id, secret, created_at) VALUES (9, x'02', ?)`, now)
	pk := `INSERT INTO passkeys (member_id, credential_id, name, credential, created_at) VALUES (?, ?, ?, '{}', ?)`
	mustExec(t, d, pk, 1, []byte("c1"), "MacBook", now)
	mustFail(t, d, "a credential another member has", pk, 2, []byte("c1"), "Phone", now)
	mustFail(t, d, "empty name", pk, 1, []byte("c2"), "", now)
	mustFail(t, d, "a passkey of no member", pk, 9, []byte("c3"), "Key", now)
}

// 00006 copies the sessions table to widen its CHECK: the rows, the user
// handle of an existing admin and the cascade from admin all survive.
func TestSecondFactorMigrationKeepsSessions(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	down(t, d, 4)
	mustExec(t, d, `INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at) VALUES (1, 'a', 'h', 'en', ?, ?)`, now, now)
	mustExec(t, d, `INSERT INTO sessions (token_hash, admin_id, created_at, last_seen_at, expires_at, created_ip, last_ip) VALUES (x'aa', 1, ?, ?, ?, 'ip', 'ip')`, now, now, now)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, dbtest.Platform); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM sessions WHERE token_hash = x'aa'`); n != 1 {
		t.Fatalf("sessions after the copy: %d", n)
	}
	if n := count(t, d, `SELECT length(webauthn_user_id) FROM members`); n != 32 {
		t.Fatalf("user handle of the existing admin: %d bytes", n)
	}
	mustExec(t, d, `DELETE FROM members`)
	if n := count(t, d, `SELECT count(*) FROM sessions`); n != 0 {
		t.Fatalf("sessions after deleting the member: %d", n)
	}
}

// 00007 makes the one admin member 1 with everything that was theirs, and
// going back keeps member 1 as the admin and drops the other members.
func TestMembersMigrationMakesTheAdminMemberOneAndBack(t *testing.T) {
	d := dbtest.Open(t)
	down(t, d, 3)
	mustExec(t, d, `INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at, jobs_seen_at, webauthn_user_id) VALUES (1, 'Tikhon', 'h', 'ru', ?, ?, ?, x'0102')`, now, now, now)
	mustExec(t, d, `INSERT INTO sessions (token_hash, admin_id, created_at, last_seen_at, expires_at, created_ip, last_ip, ended_at, end_reason) VALUES (x'aa', 1, ?, ?, ?, 'ip', 'ip', ?, 'second_factor_reset')`, now, now, now, now)
	mustExec(t, d, `INSERT INTO sessions (token_hash, admin_id, created_at, last_seen_at, expires_at, created_ip, last_ip) VALUES (x'bb', 1, ?, ?, ?, 'ip', 'ip')`, now, now, now)
	mustExec(t, d, `INSERT INTO totp (id, secret, created_at, last_step) VALUES (1, x'5ec2e7', ?, 42)`, now)
	mustExec(t, d, `INSERT INTO passkeys (credential_id, name, credential, created_at, last_used_at) VALUES (x'c1', 'MacBook', '{}', ?, ?)`, now, now)
	mustExec(t, d, `INSERT INTO sign_in_attempts (ip, time, success) VALUES ('1.1.1.1', ?, 1), ('2.2.2.2', ?, 0)`, now, now)
	up(t, d)

	var m struct {
		ID       int64  `db:"id"`
		Username string `db:"username"`
		Language string `db:"language"`
		Handle   []byte `db:"webauthn_user_id"`
		Seen     string `db:"jobs_seen_at"`
		By       string `db:"created_by"`
	}
	if err := d.R.Get(&m, `SELECT id, username, language, webauthn_user_id, jobs_seen_at, created_by FROM members`); err != nil {
		t.Fatal(err)
	}
	if m.ID != 1 || m.Username != "Tikhon" || m.Language != "ru" || string(m.Handle) != "\x01\x02" || m.Seen != now || m.By != "cli" {
		t.Fatalf("member 1: %+v", m)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM sessions WHERE member_id = 1`:                                       2,
		`SELECT count(*) FROM sessions WHERE end_reason = 'second_factor_reset'`:                  1,
		`SELECT count(*) FROM totp WHERE member_id = 1 AND secret = x'5ec2e7' AND last_step = 42`: 1,
		`SELECT count(*) FROM passkeys WHERE member_id = 1 AND credential_id = x'c1'`:             1,
		`SELECT count(*) FROM sign_in_attempts WHERE success = 1 AND member_id = 1`:               1,
		`SELECT count(*) FROM sign_in_attempts WHERE success = 0 AND member_id IS NULL`:           1,
		`SELECT count(*) FROM sqlite_master WHERE name = 'admin'`:                                 0,
	} {
		if n := count(t, d, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}

	mustExec(t, d, member, "anna", "en", now, now)
	mustExec(t, d, `INSERT INTO sessions (token_hash, member_id, created_at, last_seen_at, expires_at, created_ip, last_ip) VALUES (x'cc', 2, ?, ?, ?, 'ip', 'ip')`, now, now, now)
	mustExec(t, d, `INSERT INTO totp (member_id, secret, created_at) VALUES (2, x'02', ?)`, now)
	mustExec(t, d, `INSERT INTO passkeys (member_id, credential_id, name, credential, created_at) VALUES (2, x'c2', 'Phone', '{}', ?)`, now)
	down(t, d, 3)
	for q, want := range map[string]int{
		`SELECT count(*) FROM admin WHERE id = 1 AND username = 'Tikhon' AND jobs_seen_at = '` + now + `'`: 1,
		`SELECT count(*) FROM admin`:                                                          1,
		`SELECT count(*) FROM sessions WHERE admin_id = 1`:                                    2,
		`SELECT count(*) FROM sessions`:                                                       2,
		`SELECT count(*) FROM totp WHERE id = 1 AND secret = x'5ec2e7'`:                       1,
		`SELECT count(*) FROM totp`:                                                           1,
		`SELECT count(*) FROM passkeys`:                                                       1,
		`SELECT count(*) FROM pragma_table_info('sign_in_attempts') WHERE name = 'member_id'`: 0,
	} {
		if n := count(t, d, q); n != want {
			t.Errorf("after down: %s = %d, want %d", q, n, want)
		}
	}
	mustFail(t, d, "a second admin after going back", `INSERT INTO admin (id, username, password_hash, language, created_at, password_changed_at) VALUES (2, 'b', 'h', 'en', ?, ?)`, now, now)
	up(t, d)
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

func TestNotificationTablesAreOnePerEventChannelAndMember(t *testing.T) {
	d := dbtest.Open(t)
	mustExec(t, d, member, "tikhon", "en", now, now)
	ins := `INSERT INTO notifications (event_id, channel, member_id, lang, message, state, created_at) VALUES (7, 'telegram', ?, 'en', '{}', 'queued', ?) ON CONFLICT DO NOTHING`
	mustExec(t, d, ins, 1, now)
	mustExec(t, d, ins, 1, now)
	mustExec(t, d, ins, 2, now) // the same event to another member
	if n := count(t, d, `SELECT count(*) FROM notifications`); n != 2 {
		t.Fatalf("notifications for one event and channel to two members: %d", n)
	}
	mustFail(t, d, "unknown language", `INSERT INTO notifications (event_id, channel, member_id, lang, message, state, created_at) VALUES (8, 'telegram', 1, 'de', '{}', 'queued', ?)`, now)
	mustFail(t, d, "no recipient", `INSERT INTO notifications (event_id, channel, lang, message, state, created_at) VALUES (8, 'telegram', 'en', '{}', 'queued', ?)`, now)
	mustFail(t, d, "unknown state", `UPDATE notifications SET state = 'lost'`)
	rule := `INSERT INTO notification_rules (member_id, event_type, enabled, updated_at) VALUES (?, 'auth.signed_in', 0, ?)`
	mustExec(t, d, rule, 1, now)
	mustFail(t, d, "a rule twice for one member", rule, 1, now)
	mustFail(t, d, "a rule of nobody", rule, 9, now)
	mustFail(t, d, "rule enabled not 0/1", `UPDATE notification_rules SET enabled = 2`)
	mustFail(t, d, "a chat ID too long", `UPDATE members SET telegram_chat_id = ? WHERE id = 1`, strings.Repeat("1", 65))
	mustExec(t, d, `DELETE FROM members WHERE id = 1`)
	if n := count(t, d, `SELECT count(*) FROM notification_rules`); n != 0 {
		t.Fatalf("rules of a deleted member left: %d", n)
	}
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

func TestAccessTablesRefuseWhatTheContractForbids(t *testing.T) {
	d := dbtest.Open(t)
	mustExec(t, d, member, "anna", "en", now, now)
	acc := `INSERT INTO member_access (member_id, area, level) VALUES (?, ?, ?)`
	mustExec(t, d, acc, 1, "servers", "view")
	mustExec(t, d, acc, 1, "activity", "all")
	mustExec(t, d, acc, 1, "routerscripts", "own")
	mustFail(t, d, "a second level in one area", acc, 1, "servers", "manage")
	mustFail(t, d, "view in subscriptions", acc, 1, "subscriptions", "view")
	mustFail(t, d, "view in settings", acc, 1, "settings", "view")
	mustFail(t, d, "none stored", acc, 1, "routing", "none")
	mustFail(t, d, "an unknown area", acc, 1, "members", "manage")
	mustFail(t, d, "access of no member", acc, 9, "settings", "manage")

	mustFail(t, d, "a second anna", member, "ANNA", "en", now, now)
	mustExec(t, d, `UPDATE members SET removed_at = ?, removed_by = 'member:2' WHERE id = 1`, now)
	mustExec(t, d, member, "Anna", "en", now, now) // a removed member's username is free again
	mustFail(t, d, "removed without who removed", `UPDATE members SET removed_by = NULL WHERE id = 1`)
	pending := `INSERT INTO members (username, language, webauthn_user_id, created_at, created_by) VALUES (?, 'en', randomblob(32), ?, 'member:2')`
	mustExec(t, d, pending, "boris", now)
	mustFail(t, d, "a password without its time", `UPDATE members SET password_hash = 'h' WHERE username = 'boris'`)

	link := `INSERT INTO member_setup_links (member_id, token_lookup, token, created_at, created_by, expires_at, ended_at, end_reason) VALUES (?, ?, ?, ?, 'member:2', ?, ?, ?)`
	mustExec(t, d, link, 3, []byte("l1"), []byte("sealed"), now, now, nil, nil)
	mustFail(t, d, "a second open link of a member", link, 3, []byte("l2"), []byte("sealed"), now, now, nil, nil)
	mustFail(t, d, "a token lookup twice", link, 2, []byte("l1"), nil, now, now, now, "used")
	mustFail(t, d, "an ended link keeping its token", link, 3, []byte("l3"), []byte("sealed"), now, now, now, "used")
	mustFail(t, d, "ended without a reason", link, 3, []byte("l4"), nil, now, now, now, nil)
	mustFail(t, d, "an unknown end", link, 3, []byte("l5"), nil, now, now, now, "expired")
	mustExec(t, d, link, 3, []byte("l6"), nil, now, now, now, "replaced")

	mustExec(t, d, `INSERT INTO sessions (token_hash, member_id, created_at, last_seen_at, expires_at, created_ip, last_ip, ended_at, end_reason) VALUES (x'aa', 2, ?, ?, ?, 'ip', 'ip', ?, 'member_removed')`, now, now, now, now)
}

// 00008 copies members and the tables that point at it: everything survives,
// every member becomes Owner and the admin.* event types take member.*
// names. Going back drops removed members and those still to be set up.
func TestAccessMigrationKeepsMembersAndMakesThemOwners(t *testing.T) {
	d := dbtest.Open(t)
	down(t, d, 2)
	mustExec(t, d, member, "tikhon", "ru", now, now)
	mustExec(t, d, member, "gone", "en", now, now)
	mustExec(t, d, `DELETE FROM members WHERE username = 'gone'`)
	mustExec(t, d, `INSERT INTO sessions (token_hash, member_id, created_at, last_seen_at, expires_at, created_ip, last_ip) VALUES (x'aa', 1, ?, ?, ?, 'ip', 'ip')`, now, now, now)
	mustExec(t, d, `INSERT INTO totp (member_id, secret, created_at, last_step) VALUES (1, x'5ec2e7', ?, 42)`, now)
	mustExec(t, d, `INSERT INTO passkeys (member_id, credential_id, name, credential, created_at) VALUES (1, x'c1', 'MacBook', '{}', ?)`, now)
	ev := `INSERT INTO events (time, module, type, subject_type, subject_id, actor) VALUES (?, 'platform', ?, 'member', '1', ?)`
	mustExec(t, d, ev, now, "admin.created", "cli")
	mustExec(t, d, ev, now, "admin.language_changed", "admin")
	mustExec(t, d, `INSERT INTO notification_rules (event_type, enabled, updated_at) VALUES ('admin.language_changed', 1, ?)`, now)
	up(t, d)

	for q, want := range map[string]int{
		`SELECT count(*) FROM member_access WHERE member_id = 1`:                                            6,
		`SELECT count(*) FROM member_access WHERE member_id = 1 AND area = 'settings' AND level = 'manage'`: 1,
		`SELECT count(*) FROM sessions WHERE member_id = 1 AND token_hash = x'aa'`:                          1,
		`SELECT count(*) FROM totp WHERE member_id = 1 AND last_step = 42`:                                  1,
		`SELECT count(*) FROM passkeys WHERE member_id = 1`:                                                 1,
		`SELECT count(*) FROM events WHERE type IN ('member.added', 'member.language_changed')`:             2,
		`SELECT count(*) FROM notification_rules WHERE event_type = 'member.language_changed'`:              1,
		`SELECT count(*) FROM sqlite_master WHERE sql LIKE '%members_new%' OR sql LIKE '%members_old%'`:     0,
	} {
		if n := count(t, d, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	mustExec(t, d, member, "anna", "en", now, now)
	if n := count(t, d, `SELECT id FROM members WHERE username = 'anna'`); n != 3 {
		t.Fatalf("the next member after the copy got ID %d, want 3", n)
	}
	mustExec(t, d, `DELETE FROM members WHERE id = 1`)
	if n := count(t, d, `SELECT count(*) FROM sessions`) + count(t, d, `SELECT count(*) FROM member_access`); n != 0 {
		t.Fatalf("rows of a deleted member left: %d", n)
	}

	mustExec(t, d, member, "keep", "en", now, now)
	mustExec(t, d, `UPDATE members SET removed_at = ?, removed_by = 'member:4' WHERE username = 'anna'`, now)
	mustExec(t, d, `INSERT INTO sessions (token_hash, member_id, created_at, last_seen_at, expires_at, created_ip, last_ip, ended_at, end_reason) VALUES (x'bb', 3, ?, ?, ?, 'ip', 'ip', ?, 'member_removed')`, now, now, now, now)
	mustExec(t, d, `INSERT INTO members (username, language, webauthn_user_id, created_at, created_by) VALUES ('pending', 'en', randomblob(32), ?, 'member:4')`, now)
	mustExec(t, d, ev, now, "member.added", "member:4")
	down(t, d, 2)
	for q, want := range map[string]int{
		`SELECT count(*) FROM members`:                                      1,
		`SELECT count(*) FROM members WHERE username = 'keep'`:              1,
		`SELECT count(*) FROM sessions`:                                     0,
		`SELECT count(*) FROM events WHERE type = 'admin.created'`:          1,
		`SELECT count(*) FROM events WHERE type = 'admin.language_changed'`: 1,
	} {
		if n := count(t, d, q); n != want {
			t.Errorf("after down: %s = %d, want %d", q, n, want)
		}
	}
	up(t, d)
}

// 00009 gives the one chat and the rules there were to member 1, and makes
// every notification member 1's; going back keeps only member 1's.
func TestMemberNotificationsMigrationGivesTheChatToMemberOne(t *testing.T) {
	d := dbtest.Open(t)
	down(t, d, 1)
	mustExec(t, d, member, "tikhon", "ru", now, now)
	mustExec(t, d, `INSERT INTO settings (key, value, updated_at) VALUES ('telegram.chat_id', '-1001', ?), ('telegram.chat_title', 'Home', ?), ('telegram.bot_username', 'bot', ?)`, now, now, now)
	mustExec(t, d, `INSERT INTO notification_rules (event_type, enabled, updated_at) VALUES ('auth.locked', 0, ?)`, now)
	note := `INSERT INTO notifications (id, event_id, channel, lang, message, state, created_at) VALUES (?, ?, 'telegram', 'en', '{}', 'sent', ?)`
	mustExec(t, d, note, 40, 7, now)
	mustExec(t, d, note, 41, 8, now)
	mustExec(t, d, `DELETE FROM notifications WHERE id = 41`)
	up(t, d)

	for q, want := range map[string]int{
		`SELECT count(*) FROM members WHERE id = 1 AND telegram_chat_id = '-1001' AND telegram_chat_title = 'Home'`:  1,
		`SELECT count(*) FROM settings WHERE key IN ('telegram.chat_id', 'telegram.chat_title')`:                     0,
		`SELECT count(*) FROM settings WHERE key = 'telegram.bot_username'`:                                          1,
		`SELECT count(*) FROM notification_rules WHERE member_id = 1 AND event_type = 'auth.locked' AND enabled = 0`: 1,
		`SELECT count(*) FROM notifications WHERE id = 40 AND member_id = 1`:                                         1,
		`SELECT count(*) FROM sqlite_master WHERE instr(sql, '_new') > 0`:                                            0,
	} {
		if n := count(t, d, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	mustExec(t, d, `INSERT INTO notifications (event_id, channel, member_id, lang, message, state, created_at) VALUES (9, 'telegram', 1, 'en', '{}', 'queued', ?)`, now)
	if n := count(t, d, `SELECT max(id) FROM notifications`); n != 42 {
		t.Fatalf("the next notification after the copy got ID %d, want 42", n)
	}

	mustExec(t, d, member, "anna", "en", now, now)
	mustExec(t, d, `UPDATE members SET telegram_chat_id = '77' WHERE id = 2`)
	mustExec(t, d, `INSERT INTO notification_rules (member_id, event_type, enabled, updated_at) VALUES (2, 'auth.locked', 1, ?)`, now)
	mustExec(t, d, `INSERT INTO notifications (event_id, channel, member_id, lang, message, state, created_at) VALUES (9, 'telegram', 2, 'en', '{}', 'queued', ?)`, now)
	down(t, d, 1)
	for q, want := range map[string]int{
		`SELECT count(*) FROM settings WHERE key = 'telegram.chat_id' AND value = '-1001'`:   1,
		`SELECT count(*) FROM settings WHERE key = 'telegram.chat_title' AND value = 'Home'`: 1,
		`SELECT count(*) FROM notification_rules WHERE enabled = 0`:                          1,
		`SELECT count(*) FROM notification_rules`:                                            1,
		`SELECT count(*) FROM notifications`:                                                 2,
		`SELECT count(*) FROM pragma_table_info('members') WHERE name LIKE 'telegram_%'`:     0,
	} {
		if n := count(t, d, q); n != want {
			t.Errorf("after down: %s = %d, want %d", q, n, want)
		}
	}
	up(t, d)
}

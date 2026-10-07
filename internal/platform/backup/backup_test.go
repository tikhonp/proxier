package backup

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
)

var bg = context.Background()

type dbTx = *sqlx.Tx

type rig struct {
	t    *testing.T
	h    *jobstest.Harness
	s    *Service
	cfg  *config.Config
	stop func() // stops the job system (and its scheduler)
}

// newRig builds the service on a jobs harness. edit may change the job type
// before it is registered (tests that need a failure without a 15 minute wait).
func newRig(t *testing.T, edit func(*jobs.Type)) *rig {
	t.Helper()
	h := jobstest.New(t)
	if err := h.Events.Declare(Events...); err != nil {
		t.Fatal(err)
	}
	if err := h.Settings.Register(Section); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: t.TempDir(), TZ: time.UTC}
	s := New(h.DB, cfg, h.Settings, h.Events, dbtest.Discard)
	s.Now = h.Clock.Now
	jt := s.JobType()
	if edit != nil {
		edit(&jt)
	}
	if err := h.Sys.Register("platform", jt); err != nil {
		t.Fatal(err)
	}
	if err := h.Sys.RegisterSchedules("platform", s.Schedule()); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, h: h, s: s, cfg: cfg, stop: h.Start(h.Sys)}
}

// backUp runs one backup job to the end and returns its id.
func (r *rig) backUp() int64 {
	r.t.Helper()
	req := r.s.Request()
	req.CreatedBy = "admin"
	e, err := r.h.Sys.EnqueueNow(bg, req)
	if err != nil {
		r.t.Fatal(err)
	}
	r.h.Drain()
	return e.ID
}

func (r *rig) events(typ string) []events.Event {
	r.t.Helper()
	l, err := events.List(bg, r.h.DB.R, events.Filter{Type: typ, Limit: 100})
	if err != nil {
		r.t.Fatal(err)
	}
	return l
}

func (r *rig) names() []string {
	r.t.Helper()
	l, err := r.s.List()
	if err != nil {
		r.t.Fatal(err)
	}
	out := make([]string, len(l))
	for i, b := range l {
		out[i] = b.Name
	}
	return out
}

func TestBackupIsAConsistentCopy(t *testing.T) {
	r := newRig(t, nil)
	secret := r.h.Vault.SealString("hunter2", "setting:telegram.bot_token")
	err := r.h.DB.Write(bg, func(tx dbTx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, secret_value, updated_at) VALUES ('telegram.bot_token', ?, ?)`, secret, db.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if id := r.backUp(); r.h.State(id) != jobs.Succeeded {
		t.Fatalf("backup job: %s", r.h.State(id))
	}

	list, err := r.s.List()
	if err != nil || len(list) != 1 || list[0].Name != "proxier-2026-03-10.db" || list[0].Size == 0 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if fi, _ := os.Stat(filepath.Join(r.s.Dir(), list[0].Name)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v: it holds sealed secrets, keep it private", fi.Mode().Perm())
	}
	// Open a copy: it is a real database with the same rows, secrets still sealed.
	copyPath := filepath.Join(t.TempDir(), "proxier.db")
	data, err := os.ReadFile(filepath.Join(r.s.Dir(), list[0].Name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := db.Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cp.Close() }()
	var blob []byte
	if err := cp.R.Get(&blob, `SELECT secret_value FROM settings WHERE key = 'telegram.bot_token'`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "hunter2") {
		t.Fatal("the secret is in plain text in the backup")
	}
	if got, err := r.h.Vault.OpenString(blob, "setting:telegram.bot_token"); err != nil || got != "hunter2" {
		t.Fatalf("the master key must open the backup's secret: %q, %v", got, err)
	}
	for _, table := range []string{"settings", "events", "jobs"} {
		var live, copied int
		_ = r.h.DB.R.Get(&live, `SELECT count(*) FROM `+table)
		if err := cp.R.Get(&copied, `SELECT count(*) FROM `+table); err != nil {
			t.Fatal(err)
		}
		// The backup job itself is queued, running or just started in the live
		// database; the copy may hold fewer jobs but never different settings.
		if table == "settings" && live != copied {
			t.Fatalf("%s: %d rows live, %d in the copy", table, live, copied)
		}
	}
}

func TestBackupKeepsFourteen(t *testing.T) {
	r := newRig(t, nil)
	if err := os.MkdirAll(r.s.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// Fourteen older days are there already; today makes fifteen.
	for i := 1; i <= 14; i++ {
		day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i)
		if err := os.WriteFile(filepath.Join(r.s.Dir(), day.Format(FileLayout)), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.backUp()
	names := r.names()
	if len(names) != 14 || names[0] != "proxier-2026-03-10.db" || names[13] != "proxier-2026-02-25.db" {
		t.Fatalf("kept %d: %v", len(names), names)
	}
	if _, err := os.Stat(filepath.Join(r.s.Dir(), "proxier-2026-02-24.db")); !os.IsNotExist(err) {
		t.Fatal("the oldest backup was not removed")
	}

	// The count is a setting.
	if err := r.h.Settings.Set(bg, "admin", "backups", map[string]string{"backups.keep": "3"}); err != nil {
		t.Fatal(err)
	}
	r.h.Clock.Advance(24 * time.Hour)
	r.backUp()
	if names := r.names(); len(names) != 3 || names[0] != "proxier-2026-03-11.db" {
		t.Fatalf("keep=3: %v", names)
	}
}

func TestSameDayBackupReplaces(t *testing.T) {
	r := newRig(t, nil)
	r.backUp()
	err := r.h.DB.Write(bg, func(tx dbTx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('marker.second', 'yes', ?)`, db.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.h.Clock.Advance(2 * time.Hour) // still 2026-03-10
	r.backUp()

	if names := r.names(); len(names) != 1 {
		t.Fatalf("two backups on one day: %v", names)
	}
	data, err := os.ReadFile(filepath.Join(r.s.Dir(), "proxier-2026-03-10.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "marker.second") {
		t.Fatal("the file is the first backup, not the second")
	}
}

func TestBackupIsNamedByTheDisplayDay(t *testing.T) {
	r := newRig(t, nil)
	// 12:00 UTC on the 10th is 01:00 on the 11th in Auckland.
	if err := r.h.Settings.Set(bg, "admin", "general", map[string]string{"general.time_zone": "Pacific/Auckland"}); err != nil {
		t.Fatal(err)
	}
	r.backUp()
	if names := r.names(); len(names) != 1 || names[0] != "proxier-2026-03-11.db" {
		t.Fatalf("names: %v", names)
	}
}

func TestHalfWrittenBackupIsInvisible(t *testing.T) {
	r := newRig(t, nil)
	if err := os.MkdirAll(r.s.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{".proxier-2026-03-10.db.tmp", "proxier-2026-03-10.db.tmp", "proxier-2026-03-10.db-wal", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(r.s.Dir(), n), []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if names := r.names(); len(names) != 0 {
		t.Fatalf("listed: %v", names)
	}
	if _, _, err := r.s.Latest(); err != ErrNone {
		t.Fatalf("Latest with only leftovers: %v", err)
	}

	if err := os.WriteFile(filepath.Join(r.s.Dir(), "proxier-2026-03-09.db"), []byte("whole"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, rc, err := r.s.Latest()
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if b.Name != "proxier-2026-03-09.db" {
		t.Fatalf("Latest = %s", b.Name)
	}
	// The next backup cleans a leftover of a crashed snapshot.
	r.backUp()
	if _, err := os.Stat(filepath.Join(r.s.Dir(), ".proxier-2026-03-10.db.tmp")); !os.IsNotExist(err) {
		t.Fatal("a crashed snapshot's temporary file was kept")
	}
}

func TestBackupEvents(t *testing.T) {
	r := newRig(t, nil)
	id := r.backUp()
	done := r.events("backup.completed")
	if len(done) != 1 || done[0].Payload["file"] != "proxier-2026-03-10.db" || done[0].Payload["size"].(float64) <= 0 ||
		done[0].Subject.String() != "job:"+itoa(id) {
		t.Fatalf("completed: %+v", done)
	}
	if len(r.events("backup.failed")) != 0 {
		t.Fatal("a successful backup recorded a failure")
	}

	// A failing one: the backups path is a file.
	f := newRig(t, func(jt *jobs.Type) { jt.MaxAttempts = 1 })
	if err := os.WriteFile(filepath.Join(f.cfg.DataDir, "backups"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	fid := f.backUp()
	if f.h.State(fid) != jobs.Failed {
		t.Fatalf("job state %s", f.h.State(fid))
	}
	bad := f.events("backup.failed")
	if len(bad) != 1 || bad[0].Payload["error"] == "" || bad[0].Subject.String() != "job:"+itoa(fid) || bad[0].Actor != "job:"+itoa(fid) {
		t.Fatalf("failed: %+v", bad)
	}
	if n := len(f.events("job.failed")); n != 0 {
		t.Fatalf("%d job.failed events beside backup.failed", n)
	}
	if len(f.events("backup.completed")) != 0 {
		t.Fatal("a failed backup recorded completion")
	}
	typ, _ := f.h.Events.Lookup("backup.failed")
	if !typ.Notify {
		t.Fatal("backup.failed must notify by default")
	}
}

func TestBackupScheduleFollowsSetting(t *testing.T) {
	r := newRig(t, nil)
	r.stop() // the test drives the scheduler by hand
	nextRun := func() string {
		t.Helper()
		var next string
		if err := r.h.DB.R.Get(&next, `SELECT next_run_at FROM schedules WHERE name = 'platform.backup'`); err != nil {
			t.Fatal(err)
		}
		return next
	}
	if err := r.h.Sys.RunSchedules(bg); err != nil {
		t.Fatal(err)
	}
	// The default, 03:30, in the display zone (UTC here); it is past 12:00 today.
	if got := nextRun(); got != "2026-03-11T03:30:00.000Z" {
		t.Fatalf("default: %s", got)
	}

	// A new time in another zone applies from the next scheduling.
	if err := r.h.DB.Write(bg, func(tx dbTx) error {
		_, err := tx.Exec(`DELETE FROM schedules WHERE name = 'platform.backup'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Settings.Set(bg, "admin", "general", map[string]string{"general.time_zone": "Europe/Moscow"}); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Settings.Set(bg, "admin", "backups", map[string]string{"backups.time": "06:00"}); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Sys.RunSchedules(bg); err != nil {
		t.Fatal(err)
	}
	// 06:00 in Moscow (UTC+3) is 03:00 UTC.
	if got := nextRun(); got != "2026-03-11T03:00:00.000Z" {
		t.Fatalf("setting 06:00 Moscow: %s", got)
	}

	if err := r.h.Settings.Set(bg, "admin", "backups", map[string]string{"backups.time": "25:00"}); err == nil {
		t.Fatal("an invalid time was saved")
	}
}

func TestBackupDoesNotBlockWrites(t *testing.T) {
	r := newRig(t, nil)
	// A database big enough that the snapshot takes a while.
	err := r.h.DB.Write(bg, func(tx dbTx) error {
		if _, err := tx.Exec(`CREATE TABLE filler (id INTEGER PRIMARY KEY, data BLOB NOT NULL) STRICT`); err != nil {
			return err
		}
		_, err := tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 4000)
			INSERT INTO filler (data) SELECT zeroblob(8000) FROM n`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var snapshotting atomic.Bool
	stop := make(chan struct{})
	var during atomic.Int64
	written := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			err := r.h.DB.Write(bg, func(tx dbTx) error {
				_, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, 'x', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
					"marker.write", db.Now())
				return err
			})
			if err != nil {
				t.Error(err)
				return
			}
			select {
			case written <- struct{}{}:
			default:
			}
			if snapshotting.Load() {
				during.Add(1)
			}
			time.Sleep(time.Millisecond) // leave the job its turn on the write connection
		}
	}()
	<-written // the writer is running
	snapshotting.Store(true)
	start := time.Now()
	r.backUp()
	snapshotting.Store(false)
	close(stop)
	<-done // the writer must be gone before the rig closes the database
	t.Logf("the backup took %s; %d writes completed meanwhile", time.Since(start), during.Load())

	if during.Load() == 0 {
		t.Fatal("no write completed while the backup ran: the snapshot held the write connection")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

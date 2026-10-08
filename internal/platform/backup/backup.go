// Package backup writes nightly snapshots of the database (docs/deployment.md):
// a consistent copy made with VACUUM INTO into /data/backups, the newest 14
// kept. Secrets in a copy stay sealed, so restoring needs the same master key.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// JobType is the backup job's type name, and the coalescing key of "Back up
// now" and of the schedule alike.
const JobType = "platform.backup"

// FileLayout names a backup by its day in the display time zone.
const FileLayout = "proxier-2006-01-02.db"

var (
	namePattern = regexp.MustCompile(`^proxier-\d{4}-\d{2}-\d{2}\.db$`)
	// Half-written files carry a leading dot and a .tmp suffix; neither the
	// list nor the download ever shows them.
	tmpPattern = regexp.MustCompile(`^\.proxier-\d{4}-\d{2}-\d{2}\.db\.tmp$`)
)

// ErrNone is returned when there is no backup to download.
var ErrNone = errors.New("backup: no backups yet")

// Section is the "backups" settings section.
var Section = settings.Section{
	Name: "backups", Module: "platform",
	Fields: []settings.Field{
		{Key: "backups.time", Kind: settings.String, Default: "03:30", MaxLen: 5, Validate: validClock},
		{Key: "backups.keep", Kind: settings.Int, Default: "14", Min: 1, Max: 90},
	},
}

func validClock(v string) error {
	if _, err := time.Parse("15:04", v); err != nil || len(v) != 5 {
		return errors.New("must be a time like 03:30")
	}
	return nil
}

// Events are the event types backups record.
var Events = []events.Type{
	{Name: "backup.completed", Module: "platform", Description: "A database backup was written."},
	{Name: "backup.failed", Module: "platform", Notify: true, Emoji: "🔴", Description: "A database backup failed."},
}

// Backup is one file in the backup directory.
type Backup struct {
	Name    string // "proxier-2026-10-07.db"
	Size    int64
	ModTime time.Time
}

// Service makes and lists backups.
type Service struct {
	// Now is the clock; tests replace it.
	Now func() time.Time

	d   *db.DB
	cfg *config.Config
	st  *settings.Store
	ev  *events.Catalog
	log *slog.Logger
}

// New builds the service. The backups go to <data dir>/backups.
func New(d *db.DB, cfg *config.Config, st *settings.Store, ev *events.Catalog, log *slog.Logger) *Service {
	return &Service{Now: time.Now, d: d, cfg: cfg, st: st, ev: ev, log: log}
}

// Dir is where backups are written.
func (s *Service) Dir() string { return filepath.Join(s.cfg.DataDir, "backups") }

// JobType is the job: snapshot, then prune.
func (s *Service) JobType() jobs.Type {
	return jobs.Type{
		Name: JobType, Queue: jobs.Maintenance, MaxAttempts: 2, Backoff: []time.Duration{15 * time.Minute},
		Steps: []jobs.Step{
			{Name: "snapshot", Run: s.snapshot},
			{Name: "prune", Run: s.prune},
		},
		OnFailed: s.onFailed,
	}
}

// Schedule runs the backup daily at backups.time in the display time zone.
func (s *Service) Schedule() jobs.Schedule {
	return jobs.Schedule{
		Name: JobType, At: "03:30", Setting: "backups.time",
		Request: func(context.Context) (jobs.Request, error) { return s.Request(), nil },
	}
}

// Request is the job to enqueue; the schedule and "Back up now" share one
// coalescing key, so a second ask while one is waiting is merged into it.
func (s *Service) Request() jobs.Request {
	return jobs.Request{Type: JobType, CoalescingKey: JobType}
}

type payload struct {
	File string `json:"file"`
	Size int64  `json:"size"`
}

// location is the display time zone; a backup's name is its day there.
func (s *Service) location(ctx context.Context) *time.Location {
	name, err := s.st.Get(ctx, "general.time_zone")
	if err == nil {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return s.cfg.TZ
}

func (s *Service) snapshot(ctx context.Context, r *jobs.Run) error {
	dir := s.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	loc := s.location(ctx)
	if loc == nil {
		loc = time.UTC
	}
	name := s.Now().In(loc).Format(FileLayout)
	tmp := filepath.Join(dir, "."+name+".tmp")
	_ = os.Remove(tmp) // VACUUM INTO refuses a file with content: a leftover of a crash
	// VACUUM INTO accepts an empty file. Creating it here keeps it private
	// from the start (SQLite would create it world-readable) and turns an
	// unwritable directory into an error that names it, not SQLite's
	// "unable to open database file (14)".
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrPermission) {
		return jobs.Permanent(notWritable(dir))
	}
	if err != nil {
		return err
	}
	_ = f.Close()
	if err := s.d.VacuumInto(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := syncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The rename makes a half-written file impossible to list or download. A
	// second backup on one day replaces that day's file.
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	r.Log().Info("Wrote %s (%d bytes)", name, fi.Size())
	return r.SavePayload(ctx, payload{File: name, Size: fi.Size()})
}

// notWritable says who owns dir. Docker creates a bind mount's missing host
// directory as root, so a backup container mounting the backups directory
// before Proxier's first backup leaves one Proxier can't write; a retry won't
// change that.
func notWritable(dir string) error {
	owner := ""
	if fi, err := os.Stat(dir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf(", owned by uid %d with mode %v", st.Uid, fi.Mode().Perm())
		}
	}
	return fmt.Errorf("the backup directory %s is not writable by uid %d%s: chown it to %d", dir, os.Geteuid(), owner, os.Geteuid())
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func (s *Service) prune(ctx context.Context, r *jobs.Run) error {
	keep, err := s.st.GetInt(ctx, "backups.keep")
	if err != nil {
		return err
	}
	list, err := s.List()
	if err != nil {
		return err
	}
	for _, b := range list[min(int(keep), len(list)):] {
		if err := os.Remove(filepath.Join(s.Dir(), b.Name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		r.Log().Info("Removed %s", b.Name)
	}
	// Leftovers of a snapshot that crashed before its rename.
	entries, _ := os.ReadDir(s.Dir())
	for _, e := range entries {
		if tmpPattern.MatchString(e.Name()) {
			_ = os.Remove(filepath.Join(s.Dir(), e.Name()))
		}
	}

	var p payload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := s.ev.Record(ctx, tx, events.Event{
			Type: "backup.completed", Subject: events.Subject{Type: "job", ID: fmt.Sprint(r.Info().ID)}, Actor: r.Info().Actor(),
			Payload: map[string]any{"file": p.File, "size": p.Size},
		})
		return err
	})
}

// onFailed records backup.failed instead of the platform's job.failed.
func (s *Service) onFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, err error) error {
	msg := err.Error()
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500])
	}
	_, rerr := s.ev.Record(ctx, tx, events.Event{
		Type: "backup.failed", Subject: events.Subject{Type: "job", ID: fmt.Sprint(j.ID)}, Actor: j.Actor(),
		Payload: map[string]any{"error": msg},
	})
	return rerr
}

// List returns the backups, newest first. Half-written files are not listed.
func (s *Service) List() ([]Backup, error) {
	entries, err := os.ReadDir(s.Dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, e := range entries {
		if e.IsDir() || !namePattern.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // removed since the listing
		}
		out = append(out, Backup{Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	// The name is the date, so the newest sorts last.
	slices.SortFunc(out, func(a, b Backup) int { return -strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Latest opens the newest backup. ErrNone when there is none.
func (s *Service) Latest() (Backup, io.ReadCloser, error) {
	for range 3 { // a prune may remove the newest file between listing and opening
		list, err := s.List()
		if err != nil {
			return Backup{}, nil, err
		}
		if len(list) == 0 {
			return Backup{}, nil, ErrNone
		}
		f, err := os.Open(filepath.Join(s.Dir(), list[0].Name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Backup{}, nil, err
		}
		return list[0], f, nil
	}
	return Backup{}, nil, ErrNone
}

package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// ScheduleEnabledChanged is recorded when the admin switches a schedule.
var ScheduleEnabledChanged = events.Type{
	Name: "schedule.enabled_changed", Module: "platform", Description: "A schedule was switched on or off.",
}

// Schedule runs a job on a rhythm.
type Schedule struct {
	Name    string        // "<module>.<what>", e.g. "platform.backup"
	Every   time.Duration // or
	At      string        // "03:30" in the display time zone
	Jitter  time.Duration // random extra delay in [0, Jitter)
	Setting string        // optional settings key that overrides Every (a duration) or At ("HH:MM")
	// Request builds the job to enqueue; CreatedBy is set to schedule:<name>.
	Request func(ctx context.Context) (Request, error)
}

// RegisterSchedules adds a module's schedules.
func (s *System) RegisterSchedules(module string, sch ...Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, c := range sch {
		switch {
		case !strings.HasPrefix(c.Name, module+"."):
			errs = append(errs, fmt.Errorf("schedule %q must start with %q", c.Name, module+"."))
		case s.schedules[c.Name].Name != "":
			errs = append(errs, fmt.Errorf("schedule %q registered twice", c.Name))
		case (c.Every > 0) == (c.At != ""):
			errs = append(errs, fmt.Errorf("schedule %q needs exactly one of Every and At", c.Name))
		case c.At != "" && !validClock(c.At):
			errs = append(errs, fmt.Errorf("schedule %q: At %q is not HH:MM", c.Name, c.At))
		case c.Request == nil:
			errs = append(errs, fmt.Errorf("schedule %q has no Request", c.Name))
		default:
			s.schedules[c.Name] = c
			s.schedList = append(s.schedList, c.Name)
		}
	}
	return errors.Join(errs...)
}

func validClock(v string) bool {
	_, err := time.Parse("15:04", v)
	return err == nil && len(v) == 5
}

func (s *System) location(ctx context.Context) *time.Location {
	if s.st != nil {
		if tz, err := s.st.Get(ctx, "general.time_zone"); err == nil {
			if loc, err := time.LoadLocation(tz); err == nil {
				return loc
			}
		}
	}
	return time.UTC
}

// next is the next run of c after now, with jitter. A settings override
// replaces the code's rhythm when it parses.
func (s *System) next(ctx context.Context, c Schedule, now time.Time) time.Time {
	every, at := c.Every, c.At
	if c.Setting != "" && s.st != nil {
		if v, err := s.st.Get(ctx, c.Setting); err == nil && v != "" {
			if every > 0 {
				if d, err := time.ParseDuration(v); err == nil && d > 0 {
					every = d
				}
			} else if validClock(v) {
				at = v
			}
		}
	}
	var t time.Time
	if every > 0 {
		t = now.Add(every)
	} else {
		loc := s.location(ctx)
		tt, _ := time.Parse("15:04", at)
		local := now.In(loc)
		t = time.Date(local.Year(), local.Month(), local.Day(), tt.Hour(), tt.Minute(), 0, 0, loc)
		if !t.After(now) {
			t = time.Date(local.Year(), local.Month(), local.Day()+1, tt.Hour(), tt.Minute(), 0, 0, loc)
		}
	}
	return t.Add(s.jitter(c.Jitter))
}

func (s *System) schedulerLoop(ctx context.Context) {
	t := time.NewTicker(s.SchedulerPoll)
	defer t.Stop()
	for {
		s.tick("scheduler")
		if err := s.RunSchedules(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("jobs: scheduler", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type scheduleRow struct {
	Name        string        `db:"name"`
	Enabled     bool          `db:"enabled"`
	NextRunAt   db.Time       `db:"next_run_at"`
	LastJobID   sql.NullInt64 `db:"last_job_id"`
	LastJobLive bool          `db:"live"`
}

// RunSchedules enqueues the schedules that are due. A schedule whose last job
// is still queued, running or interrupted skips its turn and records it.
func (s *System) RunSchedules(ctx context.Context) error {
	s.mu.Lock()
	names := append([]string(nil), s.schedList...)
	s.mu.Unlock()
	var errs []error
	for _, name := range names {
		errs = append(errs, s.runSchedule(ctx, name))
	}
	return errors.Join(errs...)
}

func (s *System) runSchedule(ctx context.Context, name string) error {
	s.mu.Lock()
	c := s.schedules[name]
	s.mu.Unlock()
	now := s.Now()

	var row scheduleRow
	err := s.d.R.GetContext(ctx, &row, `
		SELECT name, enabled, next_run_at, last_job_id,
			EXISTS (SELECT 1 FROM jobs j WHERE j.id = schedules.last_job_id AND j.state IN ('queued', 'running', 'interrupted')) AS live
		FROM schedules WHERE name = ?`, name)
	if errors.Is(err, sql.ErrNoRows) {
		// First sight: the rhythm starts now, no catch-up.
		next := db.At(s.next(ctx, c, now))
		return s.d.Write(ctx, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schedules (name, next_run_at) VALUES (?, ?)`, name, next)
			return err
		})
	}
	if err != nil {
		return err
	}
	if !row.Enabled || row.NextRunAt.After(now) {
		return nil
	}
	next := db.At(s.next(ctx, c, now))

	if row.LastJobLive {
		s.log.Info("jobs: schedule skipped, its last job is still active", "schedule", name, "job", row.LastJobID.Int64)
		return s.d.Write(ctx, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				UPDATE schedules SET skipped = skipped + 1, last_skipped_at = ?, next_run_at = ? WHERE name = ?`,
				s.now(), next, name)
			return err
		})
	}
	req, err := c.Request(ctx)
	if err != nil {
		return fmt.Errorf("schedule %s: %w", name, err)
	}
	req.CreatedBy = scheduleActor(name)
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		e, err := s.Enqueue(ctx, tx, req)
		switch {
		case errors.Is(err, ErrSkipped):
			_, err = tx.ExecContext(ctx, `
				UPDATE schedules SET skipped = skipped + 1, last_skipped_at = ?, next_run_at = ? WHERE name = ?`,
				s.now(), next, name)
			return err
		case err != nil:
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE schedules SET last_run_at = ?, last_job_id = ?, next_run_at = ? WHERE name = ?`,
			s.now(), e.ID, next, name); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		s.Kick()
	}
	return err
}

// SetScheduleEnabled switches a schedule. Enabling computes the next run from
// now, so nothing is caught up. It records schedule.enabled_changed only when
// the value changes.
func (s *System) SetScheduleEnabled(ctx context.Context, name string, enabled bool, actor string) error {
	s.mu.Lock()
	c, ok := s.schedules[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("jobs: unknown schedule %q", name)
	}
	next := db.At(s.next(ctx, c, s.Now()))
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schedules (name, next_run_at) VALUES (?, ?)`, name, next); err != nil {
			return err
		}
		var cur bool
		if err := tx.GetContext(ctx, &cur, `SELECT enabled FROM schedules WHERE name = ?`, name); err != nil {
			return err
		}
		if cur == enabled {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled = ?, next_run_at = ? WHERE name = ?`, enabled, next, name); err != nil {
			return err
		}
		_, err := s.ev.Record(ctx, tx, events.Event{
			Type: ScheduleEnabledChanged.Name, Actor: actor, Subject: events.Subject{Type: "schedule", ID: name},
			Payload: map[string]any{"name": name, "enabled": enabled},
		})
		return err
	})
}

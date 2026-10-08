package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// Job is a stored job, for the UI.
type Job struct {
	Info
	State                State
	Payload              json.RawMessage
	Merged               int
	RunAfter             db.Time
	CreatedAt, StartedAt db.Time
	FinishedAt           db.Time
	Error, ErrorStep     string
	CancelRequestedBy    string
	CancelRequestedAt    db.Time
	LogLines, LogDropped int
	Quiet                bool
}

type jobRow struct {
	ID                int64          `db:"id"`
	Queue             string         `db:"queue"`
	Type              string         `db:"type"`
	ResourceKey       string         `db:"resource_key"`
	SubjectType       string         `db:"subject_type"`
	SubjectID         string         `db:"subject_id"`
	State             string         `db:"state"`
	Payload           string         `db:"payload"`
	Secrets           []byte         `db:"secrets"`
	Quiet             bool           `db:"quiet"`
	Merged            int            `db:"merged"`
	Attempt           int            `db:"attempt"`
	MaxAttempts       int            `db:"max_attempts"`
	RunAfter          db.Time        `db:"run_after"`
	BootID            string         `db:"boot_id"`
	CancelRequestedAt db.Time        `db:"cancel_requested_at"`
	CancelRequestedBy sql.NullString `db:"cancel_requested_by"`
	CreatedBy         string         `db:"created_by"`
	RetryOf           sql.NullInt64  `db:"retry_of"`
	CreatedAt         db.Time        `db:"created_at"`
	StartedAt         db.Time        `db:"started_at"`
	FinishedAt        db.Time        `db:"finished_at"`
	Error             string         `db:"error"`
	ErrorStep         string         `db:"error_step"`
	LogLines          int            `db:"log_lines"`
	LogDropped        int            `db:"log_dropped"`
}

const jobColumns = `id, queue, type, resource_key, subject_type, subject_id, state, payload, secrets, quiet, merged,
	attempt, max_attempts, run_after, boot_id, cancel_requested_at, cancel_requested_by, created_by, retry_of,
	created_at, started_at, finished_at, error, error_step, log_lines, log_dropped`

func (r jobRow) info() Info {
	return Info{
		ID: r.ID, Type: r.Type, Queue: Queue(r.Queue), Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
		ResourceKey: r.ResourceKey, Subject: events.Subject{Type: r.SubjectType, ID: r.SubjectID},
		CreatedBy: r.CreatedBy, RetryOf: r.RetryOf.Int64,
	}
}

func (r jobRow) job() Job {
	return Job{
		Info: r.info(), State: State(r.State), Payload: json.RawMessage(r.Payload), Merged: r.Merged,
		RunAfter: r.RunAfter, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Error: r.Error, ErrorStep: r.ErrorStep, CancelRequestedBy: r.CancelRequestedBy.String,
		CancelRequestedAt: r.CancelRequestedAt, LogLines: r.LogLines, LogDropped: r.LogDropped, Quiet: r.Quiet,
	}
}

func getRow(ctx context.Context, q sqlx.QueryerContext, id int64) (jobRow, error) {
	var r jobRow
	err := sqlx.GetContext(ctx, q, &r, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// Job reads one job.
func (s *System) Job(ctx context.Context, id int64) (Job, error) {
	r, err := getRow(ctx, s.d.R, id)
	if err != nil {
		return Job{}, err
	}
	return r.job(), nil
}

// Filter narrows List.
type Filter struct {
	States     []State
	Type       string
	Subject    events.Subject
	HideChecks bool
	Before     int64 // id cursor: jobs older than this id
	Limit      int
}

// List returns jobs, newest first.
func (s *System) List(ctx context.Context, f Filter) ([]Job, error) {
	var where []string
	var args []any
	if len(f.States) > 0 {
		in := make([]string, len(f.States))
		for i, st := range f.States {
			in[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "state IN ("+strings.Join(in, ",")+")")
	}
	if f.Type != "" {
		where = append(where, "type = ?")
		args = append(args, f.Type)
	}
	if f.Subject.Type != "" {
		where = append(where, "subject_type = ? AND subject_id = ?")
		args = append(args, f.Subject.Type, f.Subject.ID)
	}
	if f.HideChecks {
		where = append(where, "quiet = 0")
	}
	if f.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + jobColumns + ` FROM jobs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC LIMIT ?"
	var rows []jobRow
	if err := s.d.R.SelectContext(ctx, &rows, q, append(args, limit)...); err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	out := make([]Job, len(rows))
	for i, r := range rows {
		out[i] = r.job()
	}
	return out, nil
}

// StepState is one step of a job as the UI shows it.
type StepState struct {
	Name, State, Error    string
	Attempt               int
	StartedAt, FinishedAt db.Time
	CarriedFrom           int64
}

// Steps lists a job's steps in order.
func (s *System) Steps(ctx context.Context, id int64) ([]StepState, error) {
	var rows []struct {
		Name        string        `db:"name"`
		State       string        `db:"state"`
		Error       string        `db:"error"`
		Attempt     int           `db:"attempt"`
		StartedAt   db.Time       `db:"started_at"`
		FinishedAt  db.Time       `db:"finished_at"`
		CarriedFrom sql.NullInt64 `db:"carried_from"`
	}
	if err := s.d.R.SelectContext(ctx, &rows, `
		SELECT name, state, error, attempt, started_at, finished_at, carried_from
		FROM job_steps WHERE job_id = ? ORDER BY idx`, id); err != nil {
		return nil, err
	}
	out := make([]StepState, len(rows))
	for i, r := range rows {
		out[i] = StepState{r.Name, r.State, r.Error, r.Attempt, r.StartedAt, r.FinishedAt, r.CarriedFrom.Int64}
	}
	return out, nil
}

// LogLine is one stored line.
type LogLine struct {
	ID      int64
	Seq     int
	Time    db.Time
	Level   string
	Step    string
	Attempt int
	Text    string
}

type logRow struct {
	ID      int64   `db:"id"`
	Seq     int     `db:"seq"`
	Time    db.Time `db:"time"`
	Level   string  `db:"level"`
	Step    string  `db:"step"`
	Attempt int     `db:"attempt"`
	Text    string  `db:"text"`
}

// LogAfter returns up to limit lines with an id above afterLineID, oldest
// first. The marker shows only once something was dropped.
func (s *System) LogAfter(ctx context.Context, id, afterLineID int64, limit int) ([]LogLine, error) {
	var rows []logRow
	if err := s.d.R.SelectContext(ctx, &rows, `
		SELECT l.id, l.seq, l.time, l.level, l.step, l.attempt, l.text
		FROM job_log_lines l JOIN jobs j ON j.id = l.job_id
		WHERE l.job_id = ? AND l.id > ? AND (l.level != 'marker' OR j.log_dropped > 0)
		ORDER BY l.id LIMIT ?`, id, afterLineID, limit); err != nil {
		return nil, err
	}
	return toLines(rows), nil
}

// LogTail returns the last limit lines, oldest first.
func (s *System) LogTail(ctx context.Context, id int64, limit int) ([]LogLine, error) {
	var rows []logRow
	if err := s.d.R.SelectContext(ctx, &rows, `
		SELECT * FROM (
			SELECT l.id, l.seq, l.time, l.level, l.step, l.attempt, l.text
			FROM job_log_lines l JOIN jobs j ON j.id = l.job_id
			WHERE l.job_id = ? AND (l.level != 'marker' OR j.log_dropped > 0)
			ORDER BY l.id DESC LIMIT ?) ORDER BY id`, id, limit); err != nil {
		return nil, err
	}
	return toLines(rows), nil
}

func toLines(rows []logRow) []LogLine {
	out := make([]LogLine, len(rows))
	for i, r := range rows {
		out[i] = LogLine(r)
	}
	return out
}

// HeaderCounts feeds the header jobs cell: jobs running now, and jobs that
// failed after seenAt (every failed one when seenAt is zero).
func (s *System) HeaderCounts(ctx context.Context, seenAt db.Time) (running, failedSince int, err error) {
	if err = s.d.R.GetContext(ctx, &running, `SELECT count(*) FROM jobs WHERE state = 'running'`); err != nil {
		return
	}
	err = s.d.R.GetContext(ctx, &failedSince, `
		SELECT count(*) FROM jobs WHERE state = 'failed' AND (? IS NULL OR finished_at > ?)`, seenAt, seenAt)
	return
}

// SeenAt is when the admin last opened the Jobs page; zero if never.
func (s *System) SeenAt(ctx context.Context) (db.Time, error) {
	var t db.Time
	err := s.d.R.GetContext(ctx, &t, `SELECT jobs_seen_at FROM admin WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Time{}, nil
	}
	return t, err
}

// MarkSeen records that the admin looked at the Jobs page.
func (s *System) MarkSeen(ctx context.Context) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE admin SET jobs_seen_at = ? WHERE id = 1`, s.now())
		return err
	})
}

// Types lists the registered job types, sorted, for the Jobs page's filter.
func (s *System) Types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.types))
	for n := range s.types {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// TypeInfo says what the Jobs page needs to know about a type.
func (s *System) TypeInfo(name string) (Type, bool) { return s.typeOf(name) }

// Busy reports whether a job holding the resource key is running now. Queued
// jobs do not count: a delayed check must not wait for work that has not
// started (docs/build/README.md, Phase 1 rules).
func (s *System) Busy(ctx context.Context, key string) (bool, error) {
	return s.BusyExcept(ctx, key)
}

// BusyExcept is Busy that does not count running jobs of the given types: the
// proxy test of a round may start while the same round's self-check, which
// holds the key, is still reading the server.
func (s *System) BusyExcept(ctx context.Context, key string, types ...string) (bool, error) {
	q := `SELECT count(*) FROM jobs WHERE resource_key = ? AND state = 'running'`
	args := []any{key}
	if len(types) > 0 {
		q += ` AND type NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(types)), ",") + `)`
		for _, t := range types {
			args = append(args, t)
		}
	}
	var n int
	if err := s.d.R.GetContext(ctx, &n, q, args...); err != nil {
		return false, err
	}
	return n > 0, nil
}

// CancelQueued cancels, inside the caller's transaction, every job of a type
// that has not started and belongs to the subject (a pause cancels the resume
// job it replaces). It returns how many it cancelled.
func (s *System) CancelQueued(ctx context.Context, tx *sqlx.Tx, typ string, subject events.Subject, by string) (int, error) {
	var ids []int64
	if err := tx.SelectContext(ctx, &ids, `
		SELECT id FROM jobs WHERE type = ? AND subject_type = ? AND subject_id = ? AND state = 'queued' AND attempt = 0`,
		typ, subject.Type, subject.ID); err != nil {
		return 0, err
	}
	t, ok := s.typeOf(typ)
	for _, id := range ids {
		r, err := getRow(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		tp := &t
		if !ok {
			tp = nil
		}
		if err := s.cancelTx(ctx, tx, r.info(), tp, by, "Cancelled by "+by); err != nil {
			return 0, err
		}
		s.hub.notify(id)
	}
	return len(ids), nil
}

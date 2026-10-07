package jobs

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

const (
	// A job keeps its first HeadLines and its latest TailLines; the middle is
	// dropped and a marker line says how much.
	HeadLines = 5000
	TailLines = 5000
	// MaxLineLength is where a line is cut.
	MaxLineLength   = 4000
	truncatedSuffix = "… (truncated)"
	flushEvery      = 250 // ms
	flushLines      = 100
)

// Redacted replaces a secret in a stored line.
const Redacted = "•••"

type pending struct {
	time    db.Time
	level   string
	step    string
	attempt int
	text    string
}

// Logger writes a job's log. Lines are redacted first, cut, buffered and
// written in batches; the Job page streams them from the table.
type Logger struct {
	s     *System
	jobID int64
	now   func() db.Time

	mu      sync.Mutex
	buf     []pending
	secrets []string // longest first, with their query-escaped forms
	step    string
	attempt int
	kick    chan struct{}
}

func newLogger(s *System, jobID int64) *Logger {
	return &Logger{s: s, jobID: jobID, now: s.now, kick: make(chan struct{}, 1)}
}

func (l *Logger) Debug(format string, args ...any) { l.add("debug", fmt.Sprintf(format, args...)) }
func (l *Logger) Info(format string, args ...any)  { l.add("info", fmt.Sprintf(format, args...)) }
func (l *Logger) Warn(format string, args ...any)  { l.add("warn", fmt.Sprintf(format, args...)) }
func (l *Logger) Error(format string, args ...any) { l.add("error", fmt.Sprintf(format, args...)) }

// Redact registers more secrets for this job. Empty values are ignored.
func (l *Logger) Redact(secrets ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range secrets {
		if v == "" {
			continue
		}
		for _, form := range []string{v, url.QueryEscape(v)} {
			if !slices.Contains(l.secrets, form) {
				l.secrets = append(l.secrets, form)
			}
		}
	}
	slices.SortStableFunc(l.secrets, func(a, b string) int { return len(b) - len(a) })
}

func (l *Logger) setStep(name string, attempt int) {
	l.mu.Lock()
	l.step, l.attempt = name, attempt
	l.mu.Unlock()
}

func (l *Logger) add(level, text string) {
	l.mu.Lock()
	for _, sec := range l.secrets {
		text = strings.ReplaceAll(text, sec, Redacted)
	}
	// After redaction, so a secret across the cut is never partly stored.
	text = cutLine(text)
	l.buf = append(l.buf, pending{l.now(), level, l.step, l.attempt, text})
	full := len(l.buf) >= flushLines
	l.mu.Unlock()
	if full {
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
}

func cutLine(s string) string {
	if r := []rune(s); len(r) > MaxLineLength {
		return string(r[:MaxLineLength]) + truncatedSuffix
	}
	return s
}

// Writer splits streamed output (an SSH command's) into lines at level. Close
// writes a last line without a newline.
func (l *Logger) Writer(level string) io.WriteCloser { return &lineWriter{l: l, level: level} }

type lineWriter struct {
	l     *Logger
	level string
	part  string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.part += string(p)
	for {
		i := strings.IndexByte(w.part, '\n')
		if i < 0 {
			break
		}
		w.l.add(w.level, strings.TrimRight(w.part[:i], "\r"))
		w.part = w.part[i+1:]
	}
	return len(p), nil
}

func (w *lineWriter) Close() error {
	if w.part != "" {
		w.l.add(w.level, strings.TrimRight(w.part, "\r"))
		w.part = ""
	}
	return nil
}

// Flush writes the buffered lines.
func (l *Logger) Flush(ctx context.Context) error {
	l.mu.Lock()
	lines := l.buf
	l.buf = nil
	l.mu.Unlock()
	if len(lines) == 0 {
		return nil
	}
	err := l.s.d.Write(ctx, func(tx *sqlx.Tx) error { return appendLines(ctx, tx, l.jobID, lines) })
	if err != nil {
		l.mu.Lock()
		l.buf = append(lines, l.buf...)
		l.mu.Unlock()
		return err
	}
	l.s.hub.notify(l.jobID)
	return nil
}

// appendLines stores lines of one job inside tx, keeping the head and the tail
// and a marker for what was dropped between them.
func appendLines(ctx context.Context, tx *sqlx.Tx, jobID int64, lines []pending) error {
	var st struct {
		Count   int `db:"log_lines"`
		Dropped int `db:"log_dropped"`
	}
	if err := tx.GetContext(ctx, &st, `SELECT log_lines, log_dropped FROM jobs WHERE id = ?`, jobID); err != nil {
		return err
	}
	count := st.Count
	for _, p := range lines {
		count++
		if count == HeadLines+1 {
			// The marker sits between head and tail; its text is set below.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO job_log_lines (job_id, seq, time, level, step, attempt, text)
				VALUES (?, 0, ?, 'marker', '', 0, '')`, jobID, p.time); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO job_log_lines (job_id, seq, time, level, step, attempt, text)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, jobID, count, p.time, p.level, p.step, p.attempt, p.text); err != nil {
			return err
		}
	}
	dropped := st.Dropped
	if keep := HeadLines + TailLines; count > keep {
		dropped = count - keep
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM job_log_lines WHERE job_id = ? AND seq > ? AND seq <= ?`, jobID, HeadLines, count-TailLines); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE job_log_lines SET text = ? WHERE job_id = ? AND seq = 0`,
			fmt.Sprintf("… %d lines dropped …", dropped), jobID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET log_lines = ?, log_dropped = ? WHERE id = ?`, count, dropped, jobID)
	return err
}

// logSystem stores one line written by the system itself, not by a step.
func (s *System) logSystem(ctx context.Context, tx *sqlx.Tx, jobID int64, level, step string, attempt int, text string) error {
	return appendLines(ctx, tx, jobID, []pending{{s.now(), level, step, attempt, text}})
}

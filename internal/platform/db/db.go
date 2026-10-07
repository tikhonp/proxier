// Package db opens Proxier's SQLite database (ADR 0001): one file in WAL
// mode, opened by one process. Writes go through a single connection, reads
// through a small read-only pool.
package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ReadPoolSize is the number of read-only connections.
const ReadPoolSize = 4

// DB is the database. W is the only connection that writes: use it through
// Write. R is a read-only pool for everything that doesn't write.
//
// W has exactly one connection, so a Write callback must use its tx, never
// W itself, or it waits for itself forever.
type DB struct {
	W *sqlx.DB
	R *sqlx.DB
}

// Pragmas set on every connection.
const pragmas = "&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"

// DSN builds the data source name for the write connection: WAL, and
// transactions that take the write lock at BEGIN, so two writers never
// deadlock upgrading a read lock.
func DSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=journal_mode(WAL)" + pragmas + "&_txlock=immediate"
}

func readDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=query_only(1)" + pragmas
}

// Open opens (creating if needed) the database file at path.
func Open(path string) (*DB, error) {
	w, err := sqlx.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	// The write connection runs first so the file exists and is in WAL mode
	// before any reader opens it.
	if err := w.Ping(); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	r, err := sqlx.Open("sqlite", readDSN(path))
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	r.SetMaxOpenConns(ReadPoolSize)
	if err := r.Ping(); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	return &DB{W: w, R: r}, nil
}

// Close closes both pools.
func (d *DB) Close() error {
	return errors.Join(d.R.Close(), d.W.Close())
}

// Ping checks that both pools answer.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.W.PingContext(ctx); err != nil {
		return err
	}
	return d.R.PingContext(ctx)
}

// Write runs fn in a write transaction, committing when fn returns nil and
// rolling back otherwise. Nothing slow or remote may happen inside fn: the
// write connection is shared by the whole process.
func (d *DB) Write(ctx context.Context, fn func(tx *sqlx.Tx) error) (err error) {
	tx, err := d.W.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Time is a UTC instant stored as fixed-width text,
// "2006-01-02T15:04:05.000Z", so stored times sort and compare as strings
// and read well in the sqlite3 shell. SQL can produce the same format with
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now').
type Time struct{ time.Time }

// TimeLayout is the stored format.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Now is the current time truncated to what is stored.
func Now() Time { return At(time.Now()) }

// At converts t for storage.
func At(t time.Time) Time { return Time{t.UTC().Truncate(time.Millisecond)} }

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, nil
	}
	return t.UTC().Format(TimeLayout), nil
}

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		t.Time = time.Time{}
		return nil
	case string:
		return t.parse(v)
	case []byte:
		return t.parse(string(v))
	case time.Time:
		t.Time = v.UTC()
		return nil
	}
	return fmt.Errorf("db.Time: cannot scan %T", src)
}

func (t *Time) parse(s string) error {
	p, err := time.Parse(TimeLayout, s)
	if err != nil {
		return fmt.Errorf("db.Time: %w", err)
	}
	t.Time = p
	return nil
}

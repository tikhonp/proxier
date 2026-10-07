// Package store holds every SQL statement of the servers module. Functions
// take the connection they run on (a transaction from db.DB.Write, or the
// read pool), so a caller composes them with its events in one transaction.
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// ErrNotFound is returned when a row asked for by id does not exist.
var ErrNotFound = errors.New("servers: not found")

// Store is the module's database with its event catalog, for the operations
// that are one transaction with their event (locations).
type Store struct {
	DB     *db.DB
	Events *events.Catalog
}

// New returns a store.
func New(d *db.DB, ev *events.Catalog) *Store { return &Store{DB: d, Events: ev} }

// FieldErrors maps a form field to the i18n key of why it was refused. An
// operation that returns FieldErrors wrote nothing.
type FieldErrors map[string]string

func (fe FieldErrors) Error() string {
	parts := make([]string, 0, len(fe))
	for k, v := range fe {
		parts = append(parts, k+": "+v)
	}
	return "invalid: " + strings.Join(parts, "; ")
}

// notFound turns sql.ErrNoRows into ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// unique reports a UNIQUE constraint failure on column ("servers_locations.code").
func unique(err error, column string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: "+column)
}

// MetaGet reads a run-once marker.
func MetaGet(ctx context.Context, q sqlx.QueryerContext, key string) (string, bool, error) {
	var v string
	err := sqlx.GetContext(ctx, q, &v, `SELECT value FROM servers_meta WHERE key = ?`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// MetaSet writes a run-once marker.
func MetaSet(ctx context.Context, tx sqlx.ExtContext, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

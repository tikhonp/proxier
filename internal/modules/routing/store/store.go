// Package store holds every SQL statement of the routing module. Functions
// take the connection they run on (a transaction from db.DB.Write, or the
// read pool), so a caller composes them with its events in one transaction.
package store

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// ErrNotFound is returned when a row asked for does not exist.
var ErrNotFound = errors.New("store: not found")

// FieldErrors maps a form field to the i18n key of what is wrong with it; a
// domain row's field is "row.<n>" (from 1). An operation that returns
// FieldErrors wrote nothing.
type FieldErrors map[string]string

func (fe FieldErrors) Error() string {
	parts := make([]string, 0, len(fe))
	for k, v := range fe {
		parts = append(parts, k+": "+v)
	}
	sort.Strings(parts)
	return "invalid: " + strings.Join(parts, "; ")
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Unique reports a UNIQUE constraint failure on column ("routing_services.tag").
func Unique(err error, column string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: "+column)
}

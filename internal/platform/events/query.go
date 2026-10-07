package events

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Filter narrows List, for the Activity page.
type Filter struct {
	Module, Type, Actor string
	Subject             Subject
	Since               time.Time // zero: no lower bound
	Before              int64     // id cursor: events older than this id
	Limit               int
}

// List returns events, newest first.
func List(ctx context.Context, q sqlx.QueryerContext, f Filter) ([]Event, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		where = append(where, cond)
		args = append(args, v)
	}
	if f.Module != "" {
		add("module = ?", f.Module)
	}
	if f.Type != "" {
		add("type = ?", f.Type)
	}
	if f.Actor != "" {
		add("actor = ?", f.Actor)
	}
	if f.Subject.Type != "" {
		add("subject_type = ?", f.Subject.Type)
		if f.Subject.ID != "" {
			add("subject_id = ?", f.Subject.ID)
		}
	}
	if !f.Since.IsZero() {
		add("time >= ?", db.At(f.Since))
	}
	if f.Before > 0 {
		add("id < ?", f.Before)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, time, module, type, subject_type, subject_id, actor, payload FROM events`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	var rows []row
	if err := sqlx.SelectContext(ctx, q, &rows, query, append(args, limit)...); err != nil {
		return nil, fmt.Errorf("events: list: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		e, err := r.event()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

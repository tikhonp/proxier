package events

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Own narrows a list to one member's events or jobs (activity: own): those
// whose actor is one of Actors or whose subject is one of Subjects.
type Own struct {
	Actors   []string
	Subjects []Subject
}

// Where is the condition keeping o's rows, for a table whose actor column is
// actorCol and whose subject is subject_type and subject_id. A nil Own
// keeps every row.
func (o *Own) Where(actorCol string) (string, []any) {
	if o == nil {
		return "1", nil
	}
	var args []any
	cond := "0"
	if len(o.Actors) > 0 {
		cond = actorCol + " IN (?" + strings.Repeat(", ?", len(o.Actors)-1) + ")"
		for _, a := range o.Actors {
			args = append(args, a)
		}
	}
	if len(o.Subjects) > 0 {
		cond += " OR subject_type || ':' || subject_id IN (?" + strings.Repeat(", ?", len(o.Subjects)-1) + ")"
		for _, s := range o.Subjects {
			args = append(args, s.Type+":"+s.ID)
		}
	}
	return "(" + cond + ")", args
}

// Keeps reports whether o keeps a row of actor and subject.
func (o *Own) Keeps(actor string, subject Subject) bool {
	return o == nil || slices.Contains(o.Actors, actor) || slices.Contains(o.Subjects, subject)
}

// Filter narrows List, for the Activity page.
type Filter struct {
	Module, Type, Actor string
	Subject             Subject
	Own                 *Own      // nil: everyone's
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
	if id, ok := ActorMember(f.Actor); ok {
		// member 1 is also the old "admin"
		cond, a := (&Own{Actors: MemberActors(id)}).Where("actor")
		where = append(where, cond)
		args = append(args, a...)
	} else if f.Actor != "" {
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
	if f.Own != nil {
		cond, a := f.Own.Where("actor")
		where = append(where, cond)
		args = append(args, a...)
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

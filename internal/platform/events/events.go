// Package events records Proxier's activity history (docs/events.md). Every
// state change records an event in the same transaction as the change, so
// history and state never disagree. Events are append-only.
//
// This package records and reads events. Delivering committed events to
// subscribers (other modules' reactions, the notifier) is the dispatcher's
// job.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Actors that are not a job. A job acts as JobActor(id).
const (
	ActorAdmin  = "admin"
	ActorSystem = "system"
	ActorCLI    = "cli"
)

// JobActor is the actor of an event recorded by job id.
func JobActor(id int64) string { return fmt.Sprintf("job:%d", id) }

// Subject is what an event is about, e.g. {"server", "12"}. The zero Subject
// means the event has none (a failed sign-in).
type Subject struct {
	Type string
	ID   string
}

// String renders "server:12", or "" for no subject.
func (s Subject) String() string {
	if s.Type == "" {
		return ""
	}
	return s.Type + ":" + s.ID
}

// Event is one recorded fact.
type Event struct {
	ID      int64
	Time    db.Time
	Module  string
	Type    string
	Subject Subject
	Actor   string
	Payload map[string]any
}

// Type declares an event type a module records.
type Type struct {
	// Name is "<prefix>.<what>", e.g. "server.health_changed".
	Name string
	// Module is the module that records it.
	Module string
	// Notify is the default of the notification rule for this type.
	Notify bool
	// Description says what happened, for Settings → Notifications.
	Description string
}

// ErrUnknownType is returned when recording a type nobody declared.
var ErrUnknownType = errors.New("events: unknown event type")

// Catalog is the set of declared event types. Recording an undeclared type
// fails, so a typo cannot create a type the notifier never heard of.
type Catalog struct {
	mu    sync.RWMutex
	types map[string]Type
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog { return &Catalog{types: map[string]Type{}} }

// Declare adds event types. Declaring a name twice is an error.
func (c *Catalog) Declare(types ...Type) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for _, t := range types {
		switch {
		case t.Module == "":
			errs = append(errs, fmt.Errorf("event type %q has no module", t.Name))
		case !strings.Contains(t.Name, "."):
			errs = append(errs, fmt.Errorf("event type %q must be <prefix>.<what>", t.Name))
		case c.types[t.Name].Name != "":
			errs = append(errs, fmt.Errorf("event type %q declared twice", t.Name))
		default:
			c.types[t.Name] = t
		}
	}
	return errors.Join(errs...)
}

// Lookup returns a declared type.
func (c *Catalog) Lookup(name string) (Type, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.types[name]
	return t, ok
}

// Types returns every declared type, sorted by name.
func (c *Catalog) Types() []Type {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.SortedFunc(maps.Values(c.types), func(a, b Type) int { return strings.Compare(a.Name, b.Name) })
}

// Record writes e inside tx, the transaction of the change it describes. The
// module comes from the type's declaration and the time defaults to now. It
// returns the event's ID.
//
// The payload must never hold a secret: it is shown in Activity and feeds
// notifications.
func (c *Catalog) Record(ctx context.Context, tx sqlx.ExtContext, e Event) (int64, error) {
	t, ok := c.Lookup(e.Type)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownType, e.Type)
	}
	if e.Actor == "" {
		return 0, fmt.Errorf("events: %s recorded without an actor", e.Type)
	}
	if e.Time.IsZero() {
		e.Time = db.Now()
	}
	payload := []byte("{}")
	if len(e.Payload) > 0 {
		var err error
		if payload, err = json.Marshal(e.Payload); err != nil {
			return 0, fmt.Errorf("events: payload of %s: %w", e.Type, err)
		}
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO events (time, module, type, subject_type, subject_id, actor, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Time, t.Module, e.Type, e.Subject.Type, e.Subject.ID, e.Actor, string(payload))
	if err != nil {
		return 0, fmt.Errorf("events: record %s: %w", e.Type, err)
	}
	return res.LastInsertId()
}

type row struct {
	ID          int64   `db:"id"`
	Time        db.Time `db:"time"`
	Module      string  `db:"module"`
	Type        string  `db:"type"`
	SubjectType string  `db:"subject_type"`
	SubjectID   string  `db:"subject_id"`
	Actor       string  `db:"actor"`
	Payload     string  `db:"payload"`
}

func (r row) event() (Event, error) {
	e := Event{
		ID: r.ID, Time: r.Time, Module: r.Module, Type: r.Type,
		Subject: Subject{r.SubjectType, r.SubjectID}, Actor: r.Actor,
	}
	if err := json.Unmarshal([]byte(r.Payload), &e.Payload); err != nil {
		return Event{}, fmt.Errorf("events: payload of event %d: %w", r.ID, err)
	}
	return e, nil
}

// After returns up to limit events with an ID greater than after, oldest
// first. The dispatcher reads with it.
func After(ctx context.Context, q sqlx.QueryerContext, after int64, limit int) ([]Event, error) {
	var rows []row
	if err := sqlx.SelectContext(ctx, q, &rows, `
		SELECT id, time, module, type, subject_type, subject_id, actor, payload
		FROM events WHERE id > ? ORDER BY id LIMIT ?`, after, limit); err != nil {
		return nil, fmt.Errorf("events: read: %w", err)
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

package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Subscriber reacts to committed events (ADR 0002).
type Subscriber struct {
	Name  string   // cursor key, "<module>.<what>", e.g. "platform.notifier"
	Types []string // nil: every type
	// Handle runs inside the write transaction that advances the cursor, so a
	// reaction that only writes locally (enqueue a job, update a row) happens
	// exactly once. Nothing remote: enqueue a job instead. It must tolerate
	// seeing an event twice, because a handler that does anything else is
	// delivered at least once.
	Handle func(ctx context.Context, tx *sqlx.Tx, e Event) error
}

const (
	batchSize     = 100
	stallAfter    = 30 * time.Second
	minRetryDelay = time.Second
	maxRetryDelay = time.Minute
)

// Dispatcher delivers committed events to subscribers, one goroutine and one
// cursor each. A subscriber never skips an event: a failing handler gets the
// same event again, with backoff, and later events wait.
type Dispatcher struct {
	// Poll is how often an idle subscriber looks for new events.
	Poll func() time.Duration
	// Now is the clock for health; tests replace it.
	Now func() time.Time
	// RetryDelay is the first retry wait (1 s); it doubles up to a minute.
	RetryDelay time.Duration

	d   *db.DB
	log *slog.Logger

	mu    sync.Mutex
	subs  []Subscriber
	ticks map[string]time.Time
	begun bool
}

// NewDispatcher returns a dispatcher with no subscribers.
func NewDispatcher(d *db.DB, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		Poll: func() time.Duration { return 500 * time.Millisecond }, Now: time.Now, RetryDelay: minRetryDelay,
		d: d, log: log, ticks: map[string]time.Time{},
	}
}

// Subscribe adds a subscriber; call it before Start.
func (d *Dispatcher) Subscribe(s Subscriber) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.begun:
		return errors.New("events: Subscribe after Start")
	case s.Name == "" || s.Handle == nil:
		return errors.New("events: a subscriber needs a name and a handler")
	}
	for _, o := range d.subs {
		if o.Name == s.Name {
			return fmt.Errorf("events: subscriber %q registered twice", s.Name)
		}
	}
	d.subs = append(d.subs, s)
	return nil
}

// Start runs every subscriber until ctx ends.
func (d *Dispatcher) Start(ctx context.Context) error {
	d.mu.Lock()
	d.begun = true
	subs := slices.Clone(d.subs)
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range subs {
		if err := d.register(ctx, s); err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		wg.Add(1)
		go func() { defer wg.Done(); d.loop(ctx, s) }()
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

// register gives a subscriber seen for the first time the newest event as its
// cursor: it reacts to the future, not to history.
func (d *Dispatcher) register(ctx context.Context, s Subscriber) error {
	return d.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO event_cursors (subscriber, last_event_id, updated_at)
			VALUES (?, COALESCE((SELECT MAX(id) FROM events), 0), ?)
			ON CONFLICT (subscriber) DO NOTHING`, s.Name, db.Now())
		return err
	})
}

func (d *Dispatcher) tick(name string) {
	d.mu.Lock()
	d.ticks[name] = d.Now()
	d.mu.Unlock()
}

func (d *Dispatcher) loop(ctx context.Context, s Subscriber) {
	delay := time.Duration(0)
	for {
		d.tick(s.Name)
		n, err := d.step(ctx, s)
		wait := d.Poll()
		switch {
		case err != nil && ctx.Err() == nil:
			if delay == 0 {
				delay = d.RetryDelay
			} else {
				delay = min(delay*2, maxRetryDelay)
			}
			wait = delay
			d.log.Error("events: subscriber failed", "subscriber", s.Name, "error", err, "retry_in", delay)
			d.recordFailure(ctx, s, err)
		case err == nil:
			delay = 0
			if n == batchSize {
				wait = 0 // more waiting
			}
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		} else if ctx.Err() != nil {
			return
		}
	}
}

func (d *Dispatcher) recordFailure(ctx context.Context, s Subscriber, err error) {
	msg := err.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_ = d.d.Write(context.WithoutCancel(ctx), func(tx *sqlx.Tx) error {
		_, e := tx.ExecContext(ctx, `
			UPDATE event_cursors SET failing_since = COALESCE(failing_since, ?), last_error = ? WHERE subscriber = ?`,
			db.Now(), msg, s.Name)
		return e
	})
}

// step delivers one batch and returns how many events it read.
func (d *Dispatcher) step(ctx context.Context, s Subscriber) (int, error) {
	var cursor int64
	if err := d.d.R.GetContext(ctx, &cursor, `SELECT last_event_id FROM event_cursors WHERE subscriber = ?`, s.Name); err != nil {
		return 0, err
	}
	evs, err := After(ctx, d.d.R, cursor, batchSize)
	if err != nil || len(evs) == 0 {
		return 0, err
	}
	var skipped int64 // last event of a run that doesn't match, not yet written
	flush := func() error {
		if skipped == 0 {
			return nil
		}
		err := d.d.Write(ctx, func(tx *sqlx.Tx) error { return advance(ctx, tx, s.Name, skipped) })
		skipped = 0
		return err
	}
	for _, e := range evs {
		if s.Types != nil && !slices.Contains(s.Types, e.Type) {
			skipped = e.ID
			continue
		}
		if err := flush(); err != nil {
			return len(evs), err
		}
		err := d.d.Write(ctx, func(tx *sqlx.Tx) (err error) {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("handler panicked: %v", p)
				}
			}()
			if err := s.Handle(ctx, tx, e); err != nil {
				return err
			}
			return advance(ctx, tx, s.Name, e.ID)
		})
		if err != nil {
			return len(evs), fmt.Errorf("event %d (%s): %w", e.ID, e.Type, err)
		}
	}
	return len(evs), flush()
}

func advance(ctx context.Context, tx *sqlx.Tx, name string, id int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE event_cursors SET last_event_id = ?, updated_at = ?, failing_since = NULL, last_error = '' WHERE subscriber = ?`,
		id, db.Now(), name)
	return err
}

// Health fails when a subscriber's loop stopped ticking. A failing handler
// still ticks.
func (d *Dispatcher) Health() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.begun {
		return nil
	}
	for name, at := range d.ticks {
		if d.Now().Sub(at) > stallAfter {
			return fmt.Errorf("events: subscriber %s stalled", name)
		}
	}
	return nil
}

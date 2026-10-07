package events_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
)

type fixture struct {
	t *testing.T
	d *db.DB
	c *events.Catalog
	x *events.Dispatcher
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := dbtest.Open(t)
	x := events.NewDispatcher(d, dbtest.Discard)
	x.Poll = func() time.Duration { return 5 * time.Millisecond }
	x.RetryDelay = 5 * time.Millisecond
	return &fixture{t, d, catalog(t), x}
}

func (f *fixture) record(typ, id string) int64 {
	f.t.Helper()
	var eid int64
	err := f.d.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		eid, err = f.c.Record(ctx, tx, events.Event{Type: typ, Actor: events.ActorSystem, Subject: events.Subject{Type: "server", ID: id}})
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return eid
}

func (f *fixture) start() {
	f.t.Helper()
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := f.x.Start(c); err != nil {
			f.t.Errorf("Start: %v", err)
		}
	}()
	f.t.Cleanup(func() { cancel(); <-done })
}

func (f *fixture) cursor(name string) int64 {
	var id int64
	if err := f.d.R.Get(&id, `SELECT last_event_id FROM event_cursors WHERE subscriber = ?`, name); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// collector records the subjects it was handed.
type collector struct {
	mu  sync.Mutex
	ids []string
}

func (c *collector) add(e events.Event) {
	c.mu.Lock()
	c.ids = append(c.ids, e.Subject.ID)
	c.mu.Unlock()
}

func (c *collector) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

func TestDispatcherDeliversInOrder(t *testing.T) {
	f := newFixture(t)
	var col collector
	if err := f.x.Subscribe(events.Subscriber{Name: "test.order", Handle: func(_ context.Context, _ *sqlx.Tx, e events.Event) error {
		col.add(e)
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	f.start()
	waitFor(t, "cursor row", func() bool { var n int; _ = f.d.R.Get(&n, `SELECT count(*) FROM event_cursors`); return n == 1 })
	for i := 1; i <= 5; i++ {
		f.record("server.created", fmt.Sprint(i))
	}
	waitFor(t, "five events", func() bool { return len(col.got()) == 5 })
	for i, id := range col.got() {
		if id != fmt.Sprint(i+1) {
			t.Fatalf("order %v", col.got())
		}
	}
}

func TestDispatcherRetriesFailedEvent(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	var seen []string
	failed := false
	if err := f.x.Subscribe(events.Subscriber{Name: "test.retry", Handle: func(_ context.Context, _ *sqlx.Tx, e events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, e.Subject.ID)
		if e.Subject.ID == "2" && !failed {
			failed = true
			return errors.New("not yet")
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	f.start()
	waitFor(t, "cursor row", func() bool { var n int; _ = f.d.R.Get(&n, `SELECT count(*) FROM event_cursors`); return n == 1 })
	first := f.record("server.created", "1")
	f.record("server.created", "2")
	f.record("server.created", "3")
	waitFor(t, "everything delivered", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= 4 })
	mu.Lock()
	defer mu.Unlock()
	// 1, then 2 fails, then 2 again (same event, nothing skipped), then 3.
	want := []string{"1", "2", "2", "3"}
	for i, w := range want {
		if seen[i] != w {
			t.Fatalf("seen %v, want %v", seen, want)
		}
	}
	if f.cursor("test.retry") <= first {
		t.Fatal("cursor did not move past the retried event")
	}
}

func TestHandlerRunsInCursorTransaction(t *testing.T) {
	f := newFixture(t)
	failOnce := true
	var mu sync.Mutex
	if err := f.x.Subscribe(events.Subscriber{Name: "test.tx", Types: []string{"server.created"}, Handle: func(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
		// The reaction: record a follow-up event. Then crash once, before the cursor commits.
		if _, err := f.c.Record(ctx, tx, events.Event{Type: "server.activated", Actor: events.ActorSystem, Subject: e.Subject}); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if failOnce {
			failOnce = false
			return errors.New("crash between the effect and the cursor")
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	f.start()
	waitFor(t, "cursor row", func() bool { var n int; _ = f.d.R.Get(&n, `SELECT count(*) FROM event_cursors`); return n == 1 })
	id := f.record("server.created", "1")
	waitFor(t, "cursor", func() bool { return f.cursor("test.tx") >= id })
	time.Sleep(50 * time.Millisecond)
	var n int
	if err := f.d.R.Get(&n, `SELECT count(*) FROM events WHERE type = 'server.activated'`); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d follow-up events, want exactly one: the failed try must have rolled back", n)
	}
}

func TestNewSubscriberStartsAtNewestEvent(t *testing.T) {
	f := newFixture(t)
	for i := 1; i <= 3; i++ {
		f.record("server.created", fmt.Sprint(i))
	}
	var col collector
	if err := f.x.Subscribe(events.Subscriber{Name: "test.new", Handle: func(_ context.Context, _ *sqlx.Tx, e events.Event) error {
		col.add(e)
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	f.start()
	waitFor(t, "cursor row", func() bool { var n int; _ = f.d.R.Get(&n, `SELECT count(*) FROM event_cursors`); return n == 1 })
	f.record("server.created", "4")
	waitFor(t, "the new event", func() bool { return len(col.got()) == 1 })
	time.Sleep(30 * time.Millisecond)
	if got := col.got(); len(got) != 1 || got[0] != "4" {
		t.Fatalf("got %v: history must not be replayed", got)
	}
}

func TestSubscriberTypeFilter(t *testing.T) {
	f := newFixture(t)
	var col collector
	if err := f.x.Subscribe(events.Subscriber{Name: "test.filter", Types: []string{"server.activated"}, Handle: func(_ context.Context, _ *sqlx.Tx, e events.Event) error {
		col.add(e)
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	f.start()
	waitFor(t, "cursor row", func() bool { var n int; _ = f.d.R.Get(&n, `SELECT count(*) FROM event_cursors`); return n == 1 })
	f.record("server.created", "1")
	f.record("server.activated", "2")
	last := f.record("server.created", "3")
	waitFor(t, "cursor past the last event", func() bool { return f.cursor("test.filter") >= last })
	if got := col.got(); len(got) != 1 || got[0] != "2" {
		t.Fatalf("got %v", got)
	}
}

func TestDispatcherRefusesBadSubscribers(t *testing.T) {
	f := newFixture(t)
	h := func(context.Context, *sqlx.Tx, events.Event) error { return nil }
	if err := f.x.Subscribe(events.Subscriber{Name: "a.b", Handle: h}); err != nil {
		t.Fatal(err)
	}
	if err := f.x.Subscribe(events.Subscriber{Name: "a.b", Handle: h}); err == nil {
		t.Error("duplicate accepted")
	}
	if err := f.x.Subscribe(events.Subscriber{Name: "a.c"}); err == nil {
		t.Error("no handler accepted")
	}
}

func TestDispatcherHealth(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	now := time.Now()
	f.x.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f.x.Poll = func() time.Duration { return time.Hour } // the loop ticks once, then waits
	_ = f.x.Subscribe(events.Subscriber{Name: "a.b", Handle: func(context.Context, *sqlx.Tx, events.Event) error { return nil }})
	f.start()
	waitFor(t, "a tick", func() bool { return f.x.Health() == nil })
	time.Sleep(100 * time.Millisecond) // the one tick
	mu.Lock()
	now = now.Add(time.Minute)
	mu.Unlock()
	if f.x.Health() == nil {
		t.Fatal("a subscriber that stopped ticking must fail health")
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	f.record("server.created", "1")
	f.record("server.activated", "1")
	f.record("server.created", "2")
	last := f.record("server.activated", "2")
	list := func(fl events.Filter) []events.Event {
		t.Helper()
		out, err := events.List(ctx, f.d.R, fl)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := list(events.Filter{}); len(got) != 4 || got[0].ID != last {
		t.Fatalf("newest first: %+v", got)
	}
	if got := list(events.Filter{Type: "server.created"}); len(got) != 2 {
		t.Fatalf("type: %d", len(got))
	}
	if got := list(events.Filter{Subject: events.Subject{Type: "server", ID: "2"}}); len(got) != 2 {
		t.Fatalf("subject: %d", len(got))
	}
	if got := list(events.Filter{Module: "servers", Actor: "system", Limit: 3}); len(got) != 3 {
		t.Fatalf("limit: %d", len(got))
	}
	page := list(events.Filter{Limit: 2, Before: last})
	if len(page) != 2 || page[0].ID != last-1 {
		t.Fatalf("paging: %+v", page)
	}
	if got := list(events.Filter{Since: time.Now().Add(time.Hour)}); len(got) != 0 {
		t.Fatalf("since: %d", len(got))
	}
}

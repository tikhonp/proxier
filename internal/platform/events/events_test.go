package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
)

var ctx = context.Background()

func catalog(t *testing.T) *events.Catalog {
	t.Helper()
	c := events.NewCatalog()
	if err := c.Declare(
		events.Type{Name: "server.created", Module: "servers"},
		events.Type{Name: "server.activated", Module: "servers", Notify: true},
	); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRecordAndRead(t *testing.T) {
	d := dbtest.Open(t)
	c := catalog(t)
	var id int64
	err := d.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, err = c.Record(ctx, tx, events.Event{
			Type: "server.created", Subject: events.Subject{Type: "server", ID: "12"},
			Actor: events.JobActor(7), Payload: map[string]any{"ip": "203.0.113.7", "template_version": 3},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := events.After(ctx, d.R, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	e := got[0]
	if e.ID != id || e.Module != "servers" || e.Type != "server.created" || e.Subject.String() != "server:12" || e.Actor != "job:7" {
		t.Errorf("event = %+v", e)
	}
	if e.Payload["ip"] != "203.0.113.7" || e.Payload["template_version"] != float64(3) {
		t.Errorf("payload = %v", e.Payload)
	}
	if e.Time.IsZero() {
		t.Error("time not set")
	}
	if more, _ := events.After(ctx, d.R, id, 10); len(more) != 0 {
		t.Errorf("After(last) = %v", more)
	}
}

func TestRolledBackChangeLeavesNoEvent(t *testing.T) {
	d := dbtest.Open(t)
	c := catalog(t)
	boom := errors.New("boom")
	err := d.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := c.Record(ctx, tx, events.Event{Type: "server.created", Actor: events.ActorAdmin}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if got, _ := events.After(ctx, d.R, 0, 10); len(got) != 0 {
		t.Fatalf("events after rollback: %v", got)
	}
}

func TestUnknownTypeAndMissingActor(t *testing.T) {
	d := dbtest.Open(t)
	c := catalog(t)
	err := d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := c.Record(ctx, tx, events.Event{Type: "server.creted", Actor: events.ActorAdmin})
		return err
	})
	if !errors.Is(err, events.ErrUnknownType) {
		t.Errorf("typo: err = %v", err)
	}
	err = d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := c.Record(ctx, tx, events.Event{Type: "server.created"})
		return err
	})
	if err == nil {
		t.Error("an event without an actor was recorded")
	}
}

func TestDeclare(t *testing.T) {
	c := catalog(t)
	if err := c.Declare(events.Type{Name: "server.created", Module: "servers"}); err == nil {
		t.Error("duplicate declared")
	}
	if err := c.Declare(events.Type{Name: "nodot", Module: "x"}); err == nil {
		t.Error("name without a dot declared")
	}
	if err := c.Declare(events.Type{Name: "a.b"}); err == nil {
		t.Error("type without a module declared")
	}
	types := c.Types()
	if len(types) != 2 || types[0].Name != "server.activated" || !types[0].Notify {
		t.Errorf("Types = %+v", types)
	}
}

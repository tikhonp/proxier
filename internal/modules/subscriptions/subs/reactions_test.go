package subs_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// deliver hands an event to the subscriber inside a write, as the dispatcher does.
func deliver(t *testing.T, h *substest.Harness, typ string, serverID string, at time.Time, payload map[string]any) {
	t.Helper()
	sub := h.Mod.Subs.Subscriber()
	e := events.Event{ID: 1, Time: db.At(at), Module: "servers", Type: typ, Subject: events.Subject{Type: "server", ID: serverID}, Actor: "job:7", Payload: payload}
	if err := h.App.DB.Write(context.Background(), func(tx *sqlx.Tx) error { return sub.Handle(context.Background(), tx, e) }); err != nil {
		t.Fatal(err)
	}
}

func autoAdd(t *testing.T, h *substest.Harness, id int64) {
	t.Helper()
	st := settingsOf(get(t, h, id))
	st.AutoAdd = true
	if _, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestActivatedServerJoinsAutoSubscriptions(t *testing.T) {
	h := substest.New(t)
	family := h.Subscription("Family", 1)
	me := h.Subscription("Me", 1, 2)
	autoAdd(t, h, me)
	h.Advance(time.Minute)
	h.Catalog.Put(substest.Server(3, "nl-2", "🇳🇱", "Netherlands", 2))
	deliver(t, h, "server.activated", "3", h.Now, nil)
	if order(t, h, me) != "nl-1,de-1,nl-2" || order(t, h, family) != "nl-1" {
		t.Fatalf("me %s, family %s", order(t, h, me), order(t, h, family))
	}
	auto := func() []events.Event {
		var out []events.Event
		for _, e := range h.Events("subscription.servers_changed") {
			if e.Payload["auto"] == true {
				out = append(out, e)
			}
		}
		return out
	}
	if ev := auto(); len(ev) != 1 || ev[0].Payload["added"] != "nl-2" || ev[0].Actor != "system" || ev[0].Subject.ID != "2" {
		t.Fatalf("auto add: %+v", ev)
	}
	deliver(t, h, "server.activated", "3", h.Now, nil)
	if len(auto()) != 1 || order(t, h, me) != "nl-1,de-1,nl-2" {
		t.Error("a second delivery changed something")
	}
	// Activate anyway joins the same way
	h.Catalog.Put(substest.Server(4, "fi-1", "🇫🇮", "Finland", 1))
	deliver(t, h, "server.activated", "4", h.Now, map[string]any{"forced": true})
	if order(t, h, me) != "nl-1,de-1,nl-2,fi-1" {
		t.Errorf("forced: %s", order(t, h, me))
	}
	// a server no longer served by the time the event arrives is skipped
	deliver(t, h, "server.activated", "99", h.Now, nil)
	if len(auto()) != 2 {
		t.Errorf("%d auto adds", len(auto()))
	}
}

func TestAutoAddOnlyForLaterActivations(t *testing.T) {
	h := substest.New(t)
	me := h.Subscription("Me")
	activatedAt := h.Now
	h.Advance(time.Minute)
	autoAdd(t, h, me)
	h.Catalog.Put(substest.Server(3, "nl-2", "🇳🇱", "Netherlands", 2))
	deliver(t, h, "server.activated", "3", activatedAt, nil)
	if got := order(t, h, me); got != "" {
		t.Errorf("an earlier activation joined: %s", got)
	}
	deliver(t, h, "server.activated", "3", h.Now, nil) // the same moment counts
	if got := order(t, h, me); got != "nl-2" {
		t.Errorf("a later activation did not join: %s", got)
	}
}

func TestRetiredServerLeavesEverySubscription(t *testing.T) {
	h := substest.New(t)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	family := h.Subscription("Family", 2, 1, 3)
	friends := h.Subscription("Friends", 1)
	me := h.Subscription("Me", 2)
	h.Catalog.Drop(1)
	deliver(t, h, "server.retired", "1", h.Now, nil)
	if order(t, h, family) != "de-1,fi-1" || order(t, h, friends) != "" || order(t, h, me) != "de-1" {
		t.Fatalf("family %s friends %s me %s", order(t, h, family), order(t, h, friends), order(t, h, me))
	}
	var retired []events.Event
	for _, e := range h.Events("subscription.servers_changed") {
		if e.Payload["reason"] == "retired" {
			retired = append(retired, e)
		}
	}
	if len(retired) != 2 || retired[0].Payload["removed"] != "nl-1" || retired[0].Actor != "system" {
		t.Fatalf("retired: %+v", retired)
	}
	deliver(t, h, "server.retired", "1", h.Now, nil)
	if n := len(h.Events("subscription.servers_changed")); n != 5+2 {
		t.Errorf("a second delivery recorded: %d", n)
	}
}

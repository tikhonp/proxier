package routing_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/events"
)

var anna = access.Who{Member: 2, Grant: access.Grant{}.With(access.Routing, access.LevelOwn)}

// removed is the member.removed event of anna, removed by member 1.
func removed() events.Event {
	return events.Event{Type: "member.removed", Subject: events.Subject{Type: "member", ID: "2"},
		Payload: map[string]any{"username": "anna", "heir": float64(1), "heir_username": "admin", "sessions": float64(0)}}
}

func handOver(t *testing.T, h *routingtest.Harness) {
	t.Helper()
	sub := h.Mod.Subscribers()[0]
	if err := h.App.DB.Write(context.Background(), func(tx *sqlx.Tx) error { return sub.Handle(context.Background(), tx, removed()) }); err != nil {
		t.Fatal(err)
	}
}

// A removed member's lists, routers, custom services, Shadowrocket configs
// and discovery runs go to their heir; nothing is deleted, and a second
// delivery records nothing.
func TestRemovedMembersThingsGoToTheHeir(t *testing.T) {
	h := routingtest.New(t)
	svc := h.CustomOf(anna, "annasite", "anna.example")
	list := h.ListOf(anna, "Anna list", svc)
	_, router := h.Router("anna-router", list, routingtest.OwnedBy(anna))

	handOver(t, h)
	var left int
	if err := h.App.DB.R.Get(&left, `SELECT (SELECT count(*) FROM routing_lists WHERE owner_id = 2) +
		(SELECT count(*) FROM routing_routers WHERE owner_id = 2) + (SELECT count(*) FROM routing_services WHERE owner_id = 2)`); err != nil || left != 0 {
		t.Fatalf("anna still owns %d: %v", left, err)
	}
	if r, err := h.Mod.Routers.Get(context.Background(), router, access.Owner(1)); err != nil || r.ListID != list {
		t.Fatalf("the router is the heir's: %+v %v", r, err)
	}
	ev := h.Events("routing.handed_over")
	if len(ev) != 1 || ev[0].Subject.String() != "member:2" || ev[0].Actor != events.ActorSystem || ev[0].Payload["from"] != "anna" ||
		ev[0].Payload["to"] != "admin" || ev[0].Payload["lists"] != float64(1) || ev[0].Payload["routers"] != float64(1) ||
		ev[0].Payload["services"] != float64(1) {
		t.Fatalf("handed_over: %+v", ev)
	}
	handOver(t, h)
	if n := len(h.Events("routing.handed_over")); n != 1 {
		t.Errorf("a second delivery recorded again: %d", n)
	}
}

// A member who owns nothing has nothing handed over: no event.
func TestHandOverOfNothingRecordsNothing(t *testing.T) {
	h := routingtest.New(t)
	handOver(t, h)
	if n := len(h.Events("routing.handed_over")); n != 0 {
		t.Errorf("%d events", n)
	}
}

// Activity at activity: own shows the subjects a member owns: their lists,
// routers, configs, custom services and runs, never a shared service.
func TestOwnedSubjects(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\n")
	shared := h.Upstream("v2fly:anthropic")
	svc := h.CustomOf(anna, "annasite", "anna.example")
	list := h.ListOf(anna, "Anna list", svc, shared)
	got, err := h.Mod.OwnedSubjects(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"routing_list:" + strconv.FormatInt(list, 10): true, "service:" + strconv.FormatInt(svc, 10): true}
	if len(got) != len(want) {
		t.Fatalf("owned: %v", got)
	}
	for _, s := range got {
		if !want[s.String()] {
			t.Errorf("owned %s", s)
		}
	}
}

// The notifier asks who owns an event's subject: a member's list or custom
// service is theirs, a shared service and a thing that is gone are nobody's.
func TestOwnerOfSubjects(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	h.Up.V2fly("anthropic", "anthropic.com\n")
	shared := h.Upstream("v2fly:anthropic")
	svc := h.CustomOf(anna, "annasite", "anna.example")
	list := h.ListOf(anna, "Anna list", svc, shared)
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	for _, c := range []struct {
		subject events.Subject
		owner   int64
		ok      bool
	}{
		{events.Subject{Type: "routing_list", ID: id(list)}, 2, true},
		{events.Subject{Type: "service", ID: id(svc)}, 2, true},
		{events.Subject{Type: "service", ID: id(shared)}, 0, false},
		{events.Subject{Type: "routing_list", ID: "9999"}, 0, false},
		{events.Subject{Type: "server", ID: id(list)}, 0, false},
	} {
		owner, ok, err := h.Mod.Owner(ctx, c.subject)
		if err != nil || owner != c.owner || ok != c.ok {
			t.Errorf("%s: %d %v %v, want %d %v", c.subject, owner, ok, err, c.owner, c.ok)
		}
	}
}

// The port offers a member only what they may use, and registers a router
// of theirs on a list they may change: the default, member 1's Main, is
// refused to anna.
func TestRegistrarIsScoped(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	port := h.Mod.RouterRegistrar()
	mine := h.ListOf(anna, "Anna list")
	_, main := h.Router("home", 0)
	scope := anna.Scope(access.Routing).Writable()

	ls, err := port.Lists(ctx, scope)
	if err != nil || len(ls) != 1 || ls[0].ID != mine {
		t.Fatalf("anna's lists: %+v %v", ls, err)
	}
	if rs, err := port.Routers(ctx, scope); err != nil || len(rs) != 0 {
		t.Fatalf("anna's routers: %+v %v", rs, err)
	}
	if _, err := port.Router(ctx, main, scope); !errors.Is(err, routing.ErrRouterNotFound) {
		t.Fatalf("member 1's router through anna's scope: %v", err)
	}
	r := routerostest.New(t, h.Key())
	reg := registration("dacha", r)
	err = h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := port.Register(ctx, tx, anna, reg, "member:2")
		return err
	})
	var fe routing.FieldErrors
	if !errors.As(err, &fe) || fe["list"] == "" {
		t.Fatalf("anna registers on the default, member 1's: %v", err)
	}
	reg.ListID = mine
	var got routing.RegisteredRouter
	if err := h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		got, err = port.Register(ctx, tx, anna, reg, "member:2")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rs, err := port.Routers(ctx, scope); err != nil || len(rs) != 1 || rs[0].ID != got.ID {
		t.Fatalf("anna's routers after: %+v %v", rs, err)
	}
	none := access.Who{Member: 3}
	err = h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := port.Register(ctx, tx, none, routing.RouterRegistration{Name: "x", Host: "10.0.0.9"}, "member:3")
		return err
	})
	var forbidden *access.ForbiddenError
	if !errors.As(err, &forbidden) {
		t.Errorf("a member without routing registers: %v", err)
	}
}

// The default list is every member's start: only manage_all moves it.
func TestOwnMemberCantMakeTheirListDefault(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	mine := h.ListOf(anna, "Anna list")
	var forbidden *access.ForbiddenError
	if err := h.Mod.Lists.MakeDefault(ctx, mine, anna.Scope(access.Routing), "member:2"); !errors.As(err, &forbidden) {
		t.Fatalf("anna makes her list the default: %v", err)
	}
	if err := h.Mod.Lists.MakeDefault(ctx, mine, access.All, "member:1"); err != nil {
		t.Fatal(err)
	}
	if l, err := store.DefaultList(ctx, h.App.DB.R); err != nil || l.ID != mine {
		t.Fatalf("default: %+v %v", l, err)
	}
}

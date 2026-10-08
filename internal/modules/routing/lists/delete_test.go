package lists_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestDefaultList(t *testing.T) {
	h := routingtest.New(t)
	bg := context.Background()
	if err := h.Mod.Lists.Delete(bg, 1, 0, "admin"); !errors.Is(err, lists.ErrDefault) {
		t.Fatalf("delete Main: %v", err)
	}
	parents := h.List("Parents")
	if err := h.Mod.Lists.MakeDefault(bg, parents, "admin"); err != nil {
		t.Fatal(err)
	}
	d, err := h.Mod.Lists.Default(bg)
	if err != nil || d.ID != parents {
		t.Fatalf("default: %+v %v", d, err)
	}
	if main, _ := h.Mod.Lists.Get(bg, 1); main.Default {
		t.Error("Main is still the default")
	}
	ev := h.Events("routing.list_updated")
	if len(ev) != 1 || ev[0].Payload["changes"] != "default" || ev[0].Subject.String() != "routing_list:2" {
		t.Fatalf("events: %+v", ev)
	}
	// on the default it changes nothing
	if err := h.Mod.Lists.MakeDefault(bg, parents, "admin"); err != nil || len(h.Events("routing.list_updated")) != 1 {
		t.Errorf("again: %v", err)
	}
	if err := h.Mod.Lists.Delete(bg, parents, 0, "admin"); !errors.Is(err, lists.ErrDefault) {
		t.Errorf("delete the new default: %v", err)
	}
	if err := h.Mod.Lists.Delete(bg, 1, 0, "admin"); err != nil {
		t.Errorf("delete Main now: %v", err)
	}
}

func TestDeleteMovesTargets(t *testing.T) {
	h := routingtest.New(t)
	bg := context.Background()
	mine := h.Custom("mine", "example.com")
	parents := h.List("Parents", mine)
	h.Exec(`INSERT INTO routing_routers (name, list_id, state, host, ssh_user, created_by, created_at)
		VALUES ('Home', ?, 'active', '192.168.88.1', 'proxier', 'admin', '2026-10-08T12:00:00.000Z')`, parents)

	err := h.Mod.Lists.Delete(bg, parents, 0, "admin")
	var he *lists.HasTargetsError
	if !errors.As(err, &he) || len(he.Targets) != 1 || he.Targets[0] != "Home (router)" {
		t.Fatalf("delete: %v", err)
	}
	if err := h.Mod.Lists.Delete(bg, parents, parents, "admin"); !errors.Is(err, lists.ErrMoveTo) {
		t.Errorf("move to itself: %v", err)
	}
	marks := len(h.Marks.Changes())
	if err := h.Mod.Lists.Delete(bg, parents, 1, "admin"); err != nil {
		t.Fatal(err)
	}
	var list int64
	if err := h.App.DB.R.Get(&list, `SELECT list_id FROM routing_routers WHERE name = 'Home'`); err != nil || list != 1 {
		t.Errorf("Home follows %d: %v", list, err)
	}
	ru := h.Events("routing.router_updated")
	if len(ru) != 1 || ru[0].Payload["changes"] != "list" || ru[0].Payload["from"] != "Parents" || ru[0].Payload["to"] != "Main" ||
		ru[0].Subject.String() != "router:1" {
		t.Errorf("router_updated: %+v", ru)
	}
	ld := h.Events("routing.list_deleted")
	if len(ld) != 1 || ld[0].Payload["name"] != "Parents" || ld[0].Payload["moved"] != "Home" {
		t.Errorf("list_deleted: %+v", ld)
	}
	m := h.Marks.Changes()
	if len(m) != marks+1 || len(m[len(m)-1].Routers) != 1 || m[len(m)-1].Routers[0] != 1 {
		t.Errorf("marks: %+v", m)
	}
	if _, err := h.Mod.Lists.Get(bg, parents); !errors.Is(err, lists.ErrNotFound) {
		t.Errorf("Parents: %v", err)
	}
	// its services stay
	if _, err := h.Mod.Services.Get(bg, mine); err != nil {
		t.Errorf("mine: %v", err)
	}
}

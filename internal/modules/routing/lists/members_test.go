package lists_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestMembersAndMarks(t *testing.T) {
	h := routingtest.New(t)
	bg := context.Background()
	c := h.Custom("c", "c.com")
	a := h.Custom("a", "a.com")
	b := h.Custom("b", "b.com")
	const main = 1
	start := len(h.Marks.Changes())

	// Add appends in tag order
	if err := h.Mod.Lists.Add(bg, main, []int64{c, b}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Lists.Add(bg, main, []int64{a, b}, "admin"); err != nil { // b is in already
		t.Fatal(err)
	}
	if got := order(t, h, main); got != "b,c,a" {
		t.Fatalf("order %s", got)
	}
	ev := h.Events("routing.list_updated")
	if len(ev) != 2 || ev[0].Payload["added"] != "b, c" || ev[1].Payload["added"] != "a" || ev[0].Subject.String() != "routing_list:1" {
		t.Fatalf("added: %+v", ev)
	}
	// adding only what is in already changes nothing
	if err := h.Mod.Lists.Add(bg, main, []int64{a}, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.Events("routing.list_updated")); n != 2 {
		t.Errorf("%d events after a no-op add", n)
	}

	// Move and SetOrder
	if _, _, err := h.Mod.Lists.Move(bg, main, a, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := order(t, h, main); got != "b,a,c" {
		t.Fatalf("after move up: %s", got)
	}
	if _, _, err := h.Mod.Lists.Move(bg, main, c, true, "admin"); err != nil { // the last one down: nothing
		t.Fatal(err)
	}
	if _, _, err := h.Mod.Lists.SetOrder(bg, main, []int64{c, b, a}, nil, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := order(t, h, main); got != "c,b,a" {
		t.Fatalf("after set order: %s", got)
	}
	if _, _, err := h.Mod.Lists.SetOrder(bg, main, []int64{c, b, a}, nil, "admin"); err != nil { // the same order: nothing
		t.Fatal(err)
	}
	for _, bad := range [][]int64{{c, b}, {c, b, a, 99}, {c, c, a}} {
		if _, _, err := h.Mod.Lists.SetOrder(bg, main, bad, nil, "admin"); !errors.Is(err, lists.ErrStaleOrder) {
			t.Errorf("SetOrder(%v): %v", bad, err)
		}
	}
	if _, _, err := h.Mod.Lists.SetOrder(bg, main, []int64{a, b, c}, []int64{a, b, c}, "admin"); !errors.Is(err, lists.ErrStaleOrder) {
		t.Errorf("unexpected current order: %v", err)
	}
	if _, _, err := h.Mod.Lists.Move(bg, main, 99, true, "admin"); !errors.Is(err, lists.ErrStaleOrder) {
		t.Errorf("move a non-member: %v", err)
	}

	// Remove re-densifies
	if err := h.Mod.Lists.Remove(bg, main, b, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := order(t, h, main); got != "c,a" {
		t.Fatalf("after remove: %s", got)
	}
	if err := h.Mod.Lists.Remove(bg, main, b, "admin"); !errors.Is(err, lists.ErrNotMember) {
		t.Errorf("remove twice: %v", err)
	}

	var kinds []string
	for _, e := range h.Events("routing.list_updated") {
		switch {
		case e.Payload["added"] != nil:
			kinds = append(kinds, "added")
		case e.Payload["removed"] != nil:
			kinds = append(kinds, "removed:"+e.Payload["removed"].(string))
		case e.Payload["reordered"] == true:
			kinds = append(kinds, "reordered")
		}
	}
	if got := join(kinds); got != "added added reordered reordered removed:b" {
		t.Errorf("events: %s", got)
	}
	marks := h.Marks.Changes()[start:]
	if len(marks) != 5 {
		t.Fatalf("marks: %+v", marks)
	}
	for _, m := range marks {
		if len(m.Lists) != 1 || m.Lists[0] != main || m.Actor != "admin" || m.Why == "" {
			t.Errorf("mark %+v", m)
		}
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

func TestRemoveHandsOwnershipOverAndMarks(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	anthropic := h.Upstream("anthropic")
	mine := h.Custom("mine", "claude.ai", "example.com")
	main := h.List("Main", anthropic, mine)
	if got := owner(t, h, main, "claude.ai", false); got != "anthropic" {
		t.Fatalf("claude.ai owned by %q", got)
	}
	v, err := h.Mod.Lists.View(context.Background(), main, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := v.Members[1].Owned; o.Count() != 1 || o.Total != 2 || len(o.Dropped) != 1 || o.Dropped[0].By != "anthropic" {
		t.Errorf("mine: %+v", o)
	}
	before := len(h.Marks.Changes())
	if err := h.Mod.Lists.Remove(context.Background(), main, anthropic, "admin"); err != nil {
		t.Fatal(err)
	}
	marks := h.Marks.Changes()
	if len(marks) != before+1 || marks[len(marks)-1].Lists[0] != main {
		t.Errorf("marks: %+v", marks)
	}
	if got := owner(t, h, main, "claude.ai", false); got != "mine" {
		t.Errorf("after removing anthropic claude.ai is owned by %q", got)
	}
}

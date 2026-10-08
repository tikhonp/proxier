package subs_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func order(t *testing.T, h *substest.Harness, id int64) string {
	t.Helper()
	ms, err := h.Mod.Subs.Members(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, m := range ms {
		if m.Position != i+1 {
			t.Errorf("%s at position %d, want %d", m.Name, m.Position, i+1)
		}
		out = append(out, m.Name)
	}
	return strings.Join(out, ",")
}

func TestOnlyActiveServersAreOffered(t *testing.T) {
	h := substest.New(t)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	id := h.Subscription("Family", 1)
	h.Catalog.Drop(3) // failed or retiring: not served
	add, err := h.Mod.Subs.Addable(bg, id)
	if err != nil || len(add) != 1 || add[0].Name != "de-1" || add[0].Flag != "🇩🇪" {
		t.Fatalf("addable: %+v %v", add, err)
	}
	if err := h.Mod.Subs.AddServers(bg, id, []int64{2, 3}, "admin"); !errors.Is(err, subs.ErrNotServed) {
		t.Fatalf("posting a failed server: %v", err)
	}
	if got := order(t, h, id); got != "nl-1" {
		t.Errorf("something was added: %s", got)
	}
	if n := len(h.Events("subscription.servers_changed")); n != 1 {
		t.Errorf("%d servers_changed", n)
	}
	// the ticked ones go to the end by name, in one event
	h.Catalog.Put(substest.Server(4, "at-1", "🇦🇹", "Austria", 1))
	if err := h.Mod.Subs.AddServers(bg, id, []int64{2, 4, 1}, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := order(t, h, id); got != "nl-1,at-1,de-1" {
		t.Errorf("order %s", got)
	}
	ev := h.Events("subscription.servers_changed")
	if len(ev) != 2 || ev[1].Payload["added"] != "at-1, de-1" || ev[1].Payload["removed"] != "" {
		t.Errorf("added: %+v", ev)
	}
}

func TestReorder(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1, 2)
	reordered := func() int {
		n := 0
		for _, e := range h.Events("subscription.servers_changed") {
			if e.Payload["reordered"] == true {
				n++
			}
		}
		return n
	}
	first := func() string {
		resp, _, err := h.Mod.Subs.Preview(en(h), id, "", false)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Lines[0]
	}
	if err := h.Mod.Subs.Move(bg, id, 2, false, "admin"); err != nil { // de-1 up
		t.Fatal(err)
	}
	if order(t, h, id) != "de-1,nl-1" || !strings.HasSuffix(first(), "Germany%201") || reordered() != 1 {
		t.Fatalf("after Move: %s", order(t, h, id))
	}
	if err := h.Mod.Subs.SetOrder(bg, id, []int64{1, 2}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Subs.SetOrder(bg, id, []int64{2, 1}, "admin"); err != nil {
		t.Fatal(err)
	}
	if order(t, h, id) != "de-1,nl-1" || !strings.HasSuffix(first(), "Germany%201") || reordered() != 3 {
		t.Fatalf("after SetOrder: %s, %d", order(t, h, id), reordered())
	}
	for _, stale := range [][]int64{{2}, {2, 1, 3}, {2, 2}, {1, 3}} {
		if err := h.Mod.Subs.SetOrder(bg, id, stale, "admin"); !errors.Is(err, subs.ErrStaleOrder) {
			t.Errorf("%v: %v", stale, err)
		}
	}
	// at an end, or the same order: nothing changes, nothing is recorded
	if err := h.Mod.Subs.Move(bg, id, 2, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Subs.Move(bg, id, 1, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Subs.SetOrder(bg, id, []int64{2, 1}, "admin"); err != nil {
		t.Fatal(err)
	}
	if order(t, h, id) != "de-1,nl-1" || reordered() != 3 {
		t.Errorf("no-op moves: %s, %d", order(t, h, id), reordered())
	}
	if err := h.Mod.Subs.Move(bg, id, 9, true, "admin"); !errors.Is(err, subs.ErrStaleOrder) {
		t.Errorf("moving a non-member: %v", err)
	}
}

func TestRemoveServer(t *testing.T) {
	h := substest.New(t)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	id := h.Subscription("Family", 1, 2, 3)
	if err := h.Mod.Subs.RemoveServer(bg, id, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := order(t, h, id); got != "nl-1,fi-1" {
		t.Errorf("order %s", got)
	}
	ev := h.Events("subscription.servers_changed")
	if last := ev[len(ev)-1]; last.Payload["removed"] != "de-1" || last.Payload["added"] != "" || last.Payload["reordered"] != false {
		t.Errorf("removed: %+v", last.Payload)
	}
	if err := h.Mod.Subs.RemoveServer(bg, id, 2, "admin"); !errors.Is(err, subs.ErrNotFound) {
		t.Errorf("removing it again: %v", err)
	}
}

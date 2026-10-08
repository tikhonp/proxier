package lists_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

func TestCreateAndEdit(t *testing.T) {
	h := routingtest.New(t)
	bg := context.Background()
	id, err := h.Mod.Lists.Create(bg, "  Parents ", " for the router at my parents' ", "admin")
	if err != nil {
		t.Fatal(err)
	}
	l, err := h.Mod.Lists.Get(bg, id)
	if err != nil || l.Name != "Parents" || l.Description != "for the router at my parents'" || l.Default {
		t.Fatalf("created %+v %v", l, err)
	}
	if ev := h.Events("routing.list_created"); len(ev) != 1 || ev[0].Payload["name"] != "Parents" || ev[0].Subject.String() != "routing_list:2" {
		t.Fatalf("list_created: %+v", ev)
	}

	// refused: taken, empty, too long
	for name, want := range map[string]string{"Main": "lists.err.name_taken", "  ": "lists.err.name", strings.Repeat("x", 61): "lists.err.name"} {
		_, err := h.Mod.Lists.Create(bg, name, "", "admin")
		var fe store.FieldErrors
		if !errors.As(err, &fe) || fe["name"] != want {
			t.Errorf("create %q: %v", name, err)
		}
	}
	if _, err := h.Mod.Lists.Create(bg, "Other", strings.Repeat("d", 1001), "admin"); err == nil {
		t.Error("a long description was accepted")
	}
	// letter case counts
	if _, err := h.Mod.Lists.Create(bg, "main", "", "admin"); err != nil {
		t.Errorf("main: %v", err)
	}

	changed, err := h.Mod.Lists.Edit(bg, id, "Family", "for the family", "admin")
	if err != nil || !changed {
		t.Fatalf("edit: %v %v", changed, err)
	}
	ev := h.Events("routing.list_updated")
	if len(ev) != 1 || ev[0].Payload["changes"] != "name, description" || ev[0].Payload["from"] != "Parents" || ev[0].Payload["to"] != "Family" {
		t.Fatalf("list_updated: %+v", ev)
	}
	if changed, err := h.Mod.Lists.Edit(bg, id, " Family ", "for the family", "admin"); err != nil || changed {
		t.Errorf("unchanged edit: %v %v", changed, err)
	}
	if n := len(h.Events("routing.list_updated")); n != 1 {
		t.Errorf("an unchanged edit recorded %d events", n-1)
	}
	var fe store.FieldErrors
	if _, err := h.Mod.Lists.Edit(bg, id, "Main", "", "admin"); !errors.As(err, &fe) || fe["name"] != "lists.err.name_taken" {
		t.Errorf("rename to Main: %v", err)
	}
	if _, err := h.Mod.Lists.Edit(bg, 99, "X", "", "admin"); !errors.Is(err, lists.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if len(h.Marks.Changes()) != 0 {
		t.Errorf("names mark nothing: %+v", h.Marks.Changes())
	}
}

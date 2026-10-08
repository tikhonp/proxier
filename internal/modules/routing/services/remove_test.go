package services_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
)

func TestRemoveOnlyWhenInNoList(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	h.Up.V2fly("anthropic", "anthropic.com\n")
	id := h.Upstream("anthropic")
	h.Exec(`INSERT INTO routing_list_services (list_id, service_id, position, added_at) VALUES (1, ?, 1, '2026-10-08T12:00:00.000Z')`, id)
	err := h.Mod.Services.Remove(ctx, id, "admin")
	var in *services.InListsError
	if !errors.As(err, &in) || !slices.Equal(in.Lists, []string{"Main"}) {
		t.Fatalf("%v", err)
	}
	if _, err := h.Mod.Services.Get(ctx, id); err != nil {
		t.Fatal("removed while in Main")
	}
	h.Exec(`DELETE FROM routing_list_services`)
	if err := h.Mod.Services.Remove(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mod.Services.Get(ctx, id); !errors.Is(err, services.ErrNotFound) {
		t.Error("still there")
	}
	if n := count(t, h, `SELECT count(*) FROM routing_snapshots`); n != 0 {
		t.Errorf("%d snapshots left", n)
	}
	ev := h.Events("routing.service_removed")
	if len(ev) != 1 || ev[0].Payload["tag"] != "anthropic" || ev[0].Payload["selector"] != "v2fly:anthropic" {
		t.Errorf("%+v", ev)
	}
	if err := h.Mod.Services.Remove(ctx, id, "admin"); !errors.Is(err, services.ErrNotFound) {
		t.Error(err)
	}
}

package routers_test

import (
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestMarkerSkipsInactiveRouters(t *testing.T) {
	h := routingtest.New(t)
	main := mainList(t, h)
	_, active := h.Router("Active", 0)
	_, paused := h.Router("Paused", 0)
	h.Exec(`UPDATE routing_routers SET state = 'paused' WHERE id = ?`, paused)
	untested, err := h.Mod.Routers.Create(bg, "Untested", main, routers.Connection{Host: "10.230.9.1"}, 0, "admin")
	if err != nil {
		t.Fatal(err)
	}
	other := h.List("Other")
	_, elsewhere := h.Router("Elsewhere", other)

	synced := func() []int64 {
		var ids []int64
		for _, j := range h.JobsOf(routers.JobSync) {
			ids = append(ids, routerOf(t, j.Payload))
		}
		slices.Sort(ids)
		return slices.Compact(ids)
	}
	h.Up.V2fly("openai", "openai.com\n")
	openai := h.Upstream("v2fly:openai")
	h.List("Main", openai)
	if got := synced(); !slices.Equal(got, []int64{active}) {
		t.Fatalf("a list change: %v", got)
	}
	// a snapshot change reaches every list holding the service, still only active ones
	h.List("Other", openai)
	if got := synced(); !slices.Equal(got, []int64{active, elsewhere}) {
		t.Fatalf("after Other: %v", got)
	}
	h.Exec(`UPDATE routing_routers SET state = 'paused' WHERE id = ?`, elsewhere)
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	if _, err := h.Mod.Refresh.Refresh(bg, openai, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.JobsOf(routers.JobSync)); n != 2 {
		t.Fatalf("merged into the queued ones: %d", n)
	}
	// a router's own list change: never for one that is untested
	if err := h.Mod.Routers.SetList(bg, untested, other, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := synced(); slices.Contains(got, untested) || slices.Contains(got, paused) {
		t.Fatalf("inactive or untested: %v", got)
	}
}

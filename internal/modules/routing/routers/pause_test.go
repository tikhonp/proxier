package routers_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestPauseAndResume(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("a", "a.com\n")
	h.Up.V2fly("b", "b.com\n")
	r, id := h.Router("Home", 0)
	h.StartJobs()
	h.List("Main", h.Upstream("v2fly:a"))
	queued := h.JobsOf(routers.JobSync)
	if len(queued) != 1 {
		t.Fatalf("a delayed sync: %+v", queued)
	}

	if err := h.Mod.Routers.Pause(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Routers.Pause(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if state, _ := h.Job(queued[0].ID); state != "cancelled" {
		t.Fatalf("the queued sync is cancelled: %s", state)
	}
	if rt := router(t, h, id); rt.State != routers.StatePaused || len(h.Events("routing.router_paused")) != 1 {
		t.Fatalf("paused once: %+v", rt)
	}
	h.List("Main", h.Upstream("v2fly:b"))
	h.Advance(time.Minute)
	h.Settle()
	if len(h.JobsOf(routers.JobSync)) != 1 || len(r.Imports()) != 0 {
		t.Fatal("no sync while paused")
	}
	if _, err := h.Mod.Routers.SyncNow(bg, id, "admin"); !errors.Is(err, routers.ErrNotActive) {
		t.Fatalf("Sync now: %v", err)
	}
	if _, err := h.Mod.Routers.Preview(bg, id, "admin"); !errors.Is(err, routers.ErrNotActive) {
		t.Fatalf("Preview: %v", err)
	}
	h.RunSchedule(routers.JobDriftRound)
	h.Settle()
	if len(h.JobsOf(routers.JobDrift)) != 0 {
		t.Fatal("no drift check")
	}

	if err := h.Mod.Routers.Resume(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	if rt := router(t, h, id); rt.State != routers.StateActive || len(h.Events("routing.router_resumed")) != 1 {
		t.Fatalf("resumed: %+v", rt)
	}
	s := lastSync(t, h, id)
	if s.Trigger != routers.TriggerResume || s.State != "done" || len(r.Names("a")) != 1 || len(r.Names("b")) != 1 {
		t.Fatalf("a full sync: %+v %v", s, r.Tags())
	}
}

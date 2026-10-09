package routers_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestRemoveCleansRouter(t *testing.T) {
	h := routingtest.New(t)
	h.List("Main", h.Custom("youtube", customNames("yt", 5)...), h.Custom("mine", "example.org"))
	r, id := h.Router("Home", 0)
	r.Seed("netflix", entries("netflix.com")...)
	r.SeedUntagged("old.example", false)
	syncNow(t, h, id)
	pins := router(t, h, id).InfraPins
	if pins == 0 || len(r.Names("youtube")) != 5 {
		t.Fatalf("installed, with infra pins: %d", pins)
	}

	h.StartJobs()
	job, err := h.Mod.Routers.Remove(bg, id, true, "admin")
	if err != nil || job == 0 {
		t.Fatalf("remove: %d %v", job, err)
	}
	h.Settle()
	if state, errText := h.Job(job); state != "succeeded" {
		t.Fatalf("job: %s %s\n%s", state, errText, jobLog(t, h, job))
	}
	if len(r.Names("youtube")) != 0 || len(r.Names("mine")) != 0 {
		t.Fatalf("every applied tag is removed: %v", r.Tags())
	}
	if len(r.Names("netflix")) != 1 || r.Untagged() != 2 || !r.Holds("old.example") {
		t.Fatalf("unmanaged and untagged entries stay: %v, %d", r.Tags(), r.Untagged())
	}
	if _, err := h.Mod.Routers.Get(bg, id); !errors.Is(err, routers.ErrNotFound) {
		t.Fatalf("the router is removed: %v", err)
	}
	ev := h.Events("routing.router_removed")
	if len(ev) != 1 || ev[0].Payload["cleaned"] != true || ev[0].Payload["name"] != "Home" {
		t.Fatalf("router_removed{cleaned}: %+v", ev)
	}
}

func TestRemoveKeepAndFailure(t *testing.T) {
	h := routingtest.New(t)
	h.List("Main", h.Custom("mine", "example.org"))
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	imports := len(r.Imports())

	// Keep everything: removed at once, the router untouched
	if job, err := h.Mod.Routers.Remove(bg, id, false, "admin"); err != nil || job != 0 {
		t.Fatalf("keep: %d %v", job, err)
	}
	if _, err := h.Mod.Routers.Get(bg, id); !errors.Is(err, routers.ErrNotFound) {
		t.Fatal("removed at once")
	}
	h.Settle()
	if len(r.Imports()) != imports || len(r.Names("mine")) != 1 {
		t.Fatal("the router is untouched")
	}
	if ev := h.Events("routing.router_removed"); len(ev) != 1 || ev[0].Payload["cleaned"] != false {
		t.Fatalf("router_removed: %+v", ev)
	}

	// a failed cleaning leaves it removing, with Retry and Remove without cleaning
	for _, retry := range []bool{true, false} {
		r, id := h.Router("Office", 0)
		syncNow(t, h, id)
		r.Offline(true)
		job, err := h.Mod.Routers.Remove(bg, id, true, "admin")
		if err != nil {
			t.Fatal(err)
		}
		h.Settle()
		for _, d := range []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour} {
			h.Advance(d + time.Second)
			h.Settle()
		}
		if state, _ := h.Job(job); state != "failed" {
			t.Fatalf("the removal gives up: %s", state)
		}
		rt := router(t, h, id)
		if rt.State != routers.StateRemoving {
			t.Fatalf("still removing: %+v", rt)
		}
		failed := h.Events("routing.router_sync_failed")
		if last := failed[len(failed)-1]; last.Payload["final"] != true || !notifies(t, last) || last.Payload["manual"] != true {
			t.Fatalf("notifies when the job gives up: %+v", last)
		}
		if got, err := h.Mod.Routers.RemovalJob(bg, id); err != nil || got != job {
			t.Fatalf("Retry's job: %d %v", got, err)
		}
		if _, err := h.Mod.Routers.SyncNow(bg, id, "admin"); !errors.Is(err, routers.ErrNotActive) {
			t.Fatalf("no syncs while removing: %v", err)
		}
		if retry {
			r.Offline(false)
			if _, err := h.App.Jobs.Retry(bg, job, "admin"); err != nil {
				t.Fatal(err)
			}
			h.Settle()
			if len(r.Names("mine")) != 0 {
				t.Fatal("Retry cleans it")
			}
		} else if _, err := h.Mod.Routers.Remove(bg, id, false, "admin"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Mod.Routers.Get(bg, id); !errors.Is(err, routers.ErrNotFound) {
			t.Fatalf("removed (retry %v): %v", retry, err)
		}
		ev := h.Events("routing.router_removed")
		if last := ev[len(ev)-1]; last.Payload["cleaned"] != retry {
			t.Fatalf("cleaned %v: %+v", retry, last)
		}
	}
}

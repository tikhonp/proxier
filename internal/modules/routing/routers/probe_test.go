package routers_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// probeRound runs the probe round and waits for its probes.
func probeRound(t *testing.T, h *routingtest.Harness) {
	t.Helper()
	h.StartJobs()
	h.RunSchedule(routers.JobProbeRound)
	h.Settle()
}

// noNotifying fails when any recorded routing event would notify.
func noNotifying(t *testing.T, h *routingtest.Harness) {
	t.Helper()
	list, err := events.List(bg, h.App.DB.R, events.Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Module == "routing" && notifies(t, e) {
			t.Fatalf("%s notifies: %+v", e.Type, e)
		}
	}
}

func TestAwaitingRouterComesOnline(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	r, _, id := h.Registered("Office")
	r.Offline(true)
	rt := router(t, h, id)
	if !rt.Awaiting() || !rt.AwaitingUntil.Equal(h.Clock().Add(routers.AwaitingFor)) || rt.Connected() || rt.CreatedBy != "script" {
		t.Fatalf("awaiting setup: %+v", rt)
	}
	if ev := h.Events("routing.router_added"); len(ev) != 1 || ev[0].Payload["by"] != "script" {
		t.Fatalf("router_added: %+v", ev)
	}
	h.List("Main", h.Upstream("v2fly:openai"))
	if j := h.JobsOf(routers.JobSync); len(j) != 0 {
		t.Fatalf("a change syncs nothing: %+v", j)
	}
	probeRound(t, h)
	probes := h.JobsOf(routers.JobProbe)
	if len(probes) != 1 {
		t.Fatalf("one probe: %+v", probes)
	}
	if state, _ := h.Job(probes[0].ID); state != "failed" {
		t.Fatalf("offline: %s", state)
	}
	if rt := router(t, h, id); !rt.Awaiting() || rt.Failures != 0 {
		t.Fatalf("still awaiting, nothing counted: %+v", rt)
	}
	noNotifying(t, h)

	// day 2: the router script ran
	h.Advance(24 * time.Hour)
	r.Offline(false)
	probeRound(t, h)
	rt = router(t, h, id)
	if rt.State != routers.StateActive || !rt.Connected() || rt.Version == "" || !rt.AwaitingUntil.IsZero() {
		t.Fatalf("active: %+v", rt)
	}
	if ev := h.Events("routing.router_connected"); len(ev) != 1 || ev[0].Payload["board"] != "RB5009UG+S+" {
		t.Fatalf("router_connected: %+v", ev)
	}
	if k, err := h.App.SSH.KnownHost(bg, routingtest.Conn(r, nil).Address()); err != nil || k.Subject != routers.RouterSubject(id) {
		t.Fatalf("its key is pinned: %+v %v", k, err)
	}
	if s := lastSync(t, h, id); s.Trigger != routers.TriggerInitial || s.State != "done" || len(r.Names("openai")) != 1 {
		t.Fatalf("the initial sync: %+v", s)
	}
	noNotifying(t, h)
	// active: no more probes
	probeRound(t, h)
	if len(h.JobsOf(routers.JobProbe)) != 2 {
		t.Fatal("an active router isn't probed")
	}
}

func TestProbeLimits(t *testing.T) {
	h := routingtest.New(t)
	_, _, id := h.Registered("Parents", routingtest.ViaJump())
	probeRound(t, h)
	probes := h.JobsOf(routers.JobProbe)
	if len(probes) != 1 {
		t.Fatalf("one probe: %+v", probes)
	}
	if state, errText := h.Job(probes[0].ID); state != "failed" || errText == "" {
		t.Fatalf("an unknown jump host fails the probe: %s %q", state, errText)
	}
	if len(knownHosts(t, h)) != 0 {
		t.Fatal("a probe never pins a jump host")
	}
	if rt := router(t, h, id); !rt.Awaiting() {
		t.Fatalf("still awaiting: %+v", rt)
	}

	h.Advance(routers.AwaitingFor + time.Minute)
	probeRound(t, h)
	if len(h.JobsOf(routers.JobProbe)) != 1 {
		t.Fatal("after 7 days probes stop")
	}
	if rt := router(t, h, id); !rt.Awaiting() || rt.AwaitingUntil.After(h.Clock()) || rt.Connected() {
		t.Fatalf("never connected: %+v", rt)
	}
}

func TestTestActivatesAwaitingRouter(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, jump, id := h.Registered("Parents", routingtest.ViaJump())
	h.Advance(routers.AwaitingFor + time.Hour)

	first := runTest(t, h, id, routingtest.Conn(r, jump))
	if first.State != routers.TestConfirm || first.ConfirmHop != "jump" {
		t.Fatalf("the jump host first: %+v", first)
	}
	second := confirm(t, h, first.ID)
	if second.State != routers.TestConfirm || second.ConfirmHop != "router" {
		t.Fatalf("then the router: %+v", second)
	}
	if third := confirm(t, h, second.ID); third.State != routers.TestPassed {
		t.Fatalf("passed: %+v", third)
	}
	h.Settle()
	rt := router(t, h, id)
	if rt.State != routers.StateActive || !rt.Connected() || !rt.AwaitingUntil.IsZero() {
		t.Fatalf("active: %+v", rt)
	}
	if len(h.Events("routing.router_connected")) != 1 || len(r.Names("openai")) != 1 {
		t.Fatal("connected and synced")
	}
}

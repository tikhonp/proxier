package routers_test

import (
	"slices"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

// installed is a harness with Home following Main, which holds a custom
// youtube service of 30 names, synced.
func installed(t *testing.T) (*routingtest.Harness, int64, int64) {
	t.Helper()
	h := routingtest.New(t)
	yt := h.Custom("youtube", customNames("yt", 30)...)
	h.List("Main", yt)
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	if len(r.Names("youtube")) != 30 {
		t.Fatalf("installed: %v", r.Names("youtube"))
	}
	return h, id, yt
}

// driftRound runs the drift round and waits for its checks.
func driftRound(t *testing.T, h *routingtest.Harness) {
	t.Helper()
	h.StartJobs()
	h.RunSchedule(routers.JobDriftRound)
	h.Settle()
}

func setRepair(t *testing.T, h *routingtest.Harness, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	if err := h.App.Settings.Set(bg, "admin", "routing", map[string]string{"routing.drift_repair": v}); err != nil {
		t.Fatal(err)
	}
}

func TestDriftRepaired(t *testing.T) {
	h, id, _ := installed(t)
	r := h.RouterFake(id)
	r.DeleteEntries("youtube", 10)

	driftRound(t, h)
	ev := h.Events("routing.drift_detected")
	if len(ev) != 1 || ev[0].Payload["tags"] != "youtube" || ev[0].Payload["repair"] != true || notifies(t, ev[0]) {
		t.Fatalf("drift_detected{youtube, repair}: %+v", ev)
	}
	if got := r.Names("youtube"); len(got) != 30 {
		t.Fatalf("the repair sync reinstalls them: %d", len(got))
	}
	s := lastSync(t, h, id)
	if s.Kind != "sync" || s.Trigger != routers.TriggerDrift || s.State != "done" || s.Updated != 1 {
		t.Fatalf("repair sync: %+v", s)
	}
	syncs, _ := h.Mod.Routers.Syncs(bg, id, 0, 10)
	if !slices.ContainsFunc(syncs, func(s routers.Sync) bool { return s.Kind == "drift" && s.State == "done" }) {
		t.Fatalf("the check's row: %+v", syncs)
	}
	if rt := router(t, h, id); len(rt.Drift) != 0 || rt.DriftCheckedAt.IsZero() {
		t.Fatalf("drift cleared by the sync: %+v", rt)
	}
	// no drift now: nothing more is recorded
	driftRound(t, h)
	if len(h.Events("routing.drift_detected")) != 1 {
		t.Fatal("no drift, no event")
	}
}

func TestDriftWithoutRepairNotifiesOnce(t *testing.T) {
	h, id, _ := installed(t)
	setRepair(t, h, false)
	r := h.RouterFake(id)
	r.DeleteEntries("youtube", 10)

	driftRound(t, h)
	ev := h.Events("routing.drift_detected")
	if len(ev) != 1 || ev[0].Payload["repair"] != false || !notifies(t, ev[0]) {
		t.Fatalf("drift_detected{repair: false} notifies: %+v", ev)
	}
	if len(r.Names("youtube")) != 20 || len(h.JobsOf(routers.JobSync)) != 1 {
		t.Fatal("nothing is repaired")
	}
	if rt := router(t, h, id); !slices.Equal(rt.Drift, []string{"youtube"}) {
		t.Fatalf("stored drift: %+v", rt.Drift)
	}
	h.Advance(6 * time.Hour)
	driftRound(t, h)
	if len(h.Events("routing.drift_detected")) != 1 {
		t.Fatal("the same drift found again records nothing")
	}

	h.StartJobs()
	if _, err := h.Mod.Routers.Repair(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	if len(r.Names("youtube")) != 30 {
		t.Fatal("Repair syncs it")
	}
	if s := lastSync(t, h, id); s.Trigger != routers.TriggerDrift {
		t.Fatalf("repair: %+v", s)
	}
	if rt := router(t, h, id); len(rt.Drift) != 0 {
		t.Fatalf("drift cleared: %+v", rt.Drift)
	}
}

func TestDriftComparesWithApplied(t *testing.T) {
	h, id, yt := installed(t)
	setRepair(t, h, false)
	r := h.RouterFake(id)

	// a pending desired change isn't drift: its own sync handles it
	h.StopJobs()
	setCustom(t, h, yt, "youtube", customNames("yt", 31)...)
	for _, j := range h.JobsOf(routers.JobSync) {
		if j.State == "queued" {
			if err := h.App.Jobs.Cancel(bg, j.ID, "admin"); err != nil {
				t.Fatal(err)
			}
		}
	}
	driftRound(t, h)
	if ev := h.Events("routing.drift_detected"); len(ev) != 0 {
		t.Fatalf("a pending change is not drift: %+v", ev)
	}
	if rt := router(t, h, id); len(rt.Drift) != 0 || rt.DriftCheckedAt.IsZero() {
		t.Fatalf("checked, no drift: %+v", rt)
	}

	// a hand-edited forward-to is
	r.EditForwardTo("youtube", "yt-3.com", "elsewhere")
	driftRound(t, h)
	if ev := h.Events("routing.drift_detected"); len(ev) != 1 || ev[0].Payload["tags"] != "youtube" {
		t.Fatalf("forward-to drift: %+v", ev)
	}
}

func TestDriftRoundSkips(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("a", "a.com\n")
	other := h.List("Other")
	_, queued := h.Router("Queued", other)
	_, paused := h.Router("Paused", 0)
	_, _, awaiting := h.Registered("Awaiting")
	_, ok := h.Router("Ok", 0)
	off, offline := h.Router("Offline", 0)
	off.Offline(true)
	if err := h.Mod.Routers.Pause(bg, paused, "admin"); err != nil {
		t.Fatal(err)
	}
	h.List("Other", h.Upstream("v2fly:a")) // Queued gets a sync 30 s later

	driftRound(t, h)
	var checked []int64
	for _, j := range h.JobsOf(routers.JobDrift) {
		checked = append(checked, routerOf(t, j.Payload))
	}
	slices.Sort(checked)
	if !slices.Equal(checked, []int64{ok, offline}) {
		t.Fatalf("checked %v; queued %d, paused %d, awaiting %d skipped", checked, queued, paused, awaiting)
	}
	rt := router(t, h, offline)
	if rt.Failures != 0 || rt.LastResult != "" || len(h.Events("routing.router_sync_failed")) != 0 || len(h.Events("routing.drift_detected")) != 0 {
		t.Fatalf("a check that can't connect counts no failure: %+v", rt)
	}
	if s := lastSync(t, h, offline); s.Kind != "drift" || s.State != "failed" || s.Step != routers.StepConnect {
		t.Fatalf("its row fails: %+v", s)
	}
}

package routing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestDashboardRoutingArea(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	dash := func() string {
		t.Helper()
		rec := h.Login.Get("/")
		if rec.Code != 200 {
			t.Fatalf("dashboard: %d", rec.Code)
		}
		return rec.Body.String()
	}
	if strings.Contains(dash(), "Routers →") {
		t.Fatal("nothing configured, no area")
	}

	h.Up.V2fly("netflix", lines("nf", 40))
	nf := h.Upstream("v2fly:netflix")
	h.List("Main", nf, h.Custom("youtube", strings.Fields(lines("yt", 30))...))
	_, home := h.Router("Home", 0)
	_, office := h.Router("Office", 0)
	h.StartJobs()
	for _, id := range []int64{home, office} {
		if _, err := h.Mod.Routers.SyncNow(ctx, id, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	h.Settle()
	body := dash()
	for _, s := range []string{">Routing<", "Routers →", "2 routers in sync", ">Home<", ">Office<", "synced", "Snapshots",
		"None waiting for a decision.", "Daily refresh", "not run yet"} {
		if !strings.Contains(body, s) {
			t.Errorf("all well: no %q", s)
		}
	}

	// failing, drift and unmanaged routers first; the synced ones leave
	h.RouterFake(home).Offline(true)
	if _, err := h.Mod.Routers.SyncNow(ctx, home, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	h.RouterFake(office).DeleteEntries("youtube", 3)
	h.RouterFake(office).Seed("old-work", routeros.Entry{Name: "work.example"})
	if err := h.App.Settings.Set(ctx, "admin", "routing", map[string]string{"routing.drift_repair": "false"}); err != nil {
		t.Fatal(err)
	}
	h.RunSchedule(routers.JobDriftRound)
	h.Settle()
	_, synced := h.Router("Third", 0)
	if _, err := h.Mod.Routers.SyncNow(ctx, synced, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	body = dash()
	for _, s := range []string{">Home<", "sync failed at connect", ">Office<", "drift: youtube", "1 router in sync"} {
		if !strings.Contains(body, s) {
			t.Errorf("attention: no %q", s)
		}
	}
	if strings.Contains(body, ">Third<") {
		t.Error("a synced router leaves while others need a look")
	}

	// a snapshot waiting for a decision, the last digest
	h.Up.V2fly("netflix", lines("nf", 4))
	h.RunSchedule("routing.refresh_round")
	h.Settle()
	body = dash()
	for _, s := range []string{">netflix<", "90 % drop", "Review", "0 services changed (+0 / −0 domains) · 1 rejected"} {
		if !strings.Contains(body, s) {
			t.Errorf("snapshots and digest: no %q", s)
		}
	}
}

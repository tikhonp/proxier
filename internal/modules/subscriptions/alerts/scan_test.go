package alerts_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/alerts"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

var bg = context.Background()

// apps are user agents of four different families.
var apps = []string{"Happ/1.6.1", "v2RayTun/4.0", "HiddifyNext/2.5", "Shadowrocket/2.2"}

func link(t *testing.T, h *substest.Harness) int64 {
	t.Helper()
	id, _ := h.Link(h.Subscription("Friends", 1), "Alex")
	return id
}

// net is an address in the n-th /24.
func net(n int) string { return fmt.Sprintf("198.51.%d.7", n) }

func scan(t *testing.T, h *substest.Harness) {
	t.Helper()
	if err := h.Mod.Alerts.Scan(bg, h.Now, "job:1"); err != nil {
		t.Fatal(err)
	}
}

func alertsOf(h *substest.Harness) []events.Event { return h.Events("link.shared_suspected") }

func TestSameNetworkCountsOnce(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	for i := range 10 {
		h.FetchAt(id, h.Now.Add(-time.Duration(i)*time.Minute), "198.51.100."+strconv.Itoa(10+i), apps[0])
	}
	scan(t, h)
	if n := len(alertsOf(h)); n != 0 {
		t.Errorf("one /24 alerted: %d", n)
	}
	c, err := h.Mod.Alerts.Counts(bg, id, h.Now.Add(-alerts.Window))
	if err != nil || len(c.Networks) != 1 || c.Networks[0].Key != "198.51.100.0/24" || c.Networks[0].Fetches != 10 {
		t.Errorf("counts: %+v %v", c, err)
	}
}

func TestFiveNetworksAlert(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	for i := range 4 {
		h.FetchAt(id, h.Now.Add(-time.Hour), net(i), apps[0])
	}
	scan(t, h)
	if n := len(alertsOf(h)); n != 0 {
		t.Fatalf("4 networks alerted")
	}
	h.FetchAt(id, h.Now, net(4), apps[0])
	scan(t, h)
	evs := alertsOf(h)
	if len(evs) != 1 {
		t.Fatalf("5 networks: %d alerts", len(evs))
	}
	e := evs[0]
	if e.Actor != "job:1" || e.Payload["networks"] != float64(5) || e.Payload["apps"] != float64(1) || e.Payload["window"] != "24h" {
		t.Errorf("event: %+v", e.Payload)
	}
	if strings.Contains(fmt.Sprint(e.Payload), "198.51.") {
		t.Errorf("the event names an IP: %v", e.Payload)
	}
	l, _ := h.Mod.Links.Get(bg, id)
	if !h.Mod.Alerts.HasAlert(l, h.Now) {
		t.Error("the link has no alert")
	}
}

func TestFourAppsAlert(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	for i, ua := range apps {
		h.FetchAt(id, h.Now.Add(-time.Hour), net(i), ua)
	}
	scan(t, h)
	evs := alertsOf(h)
	if len(evs) != 1 || evs[0].Payload["networks"] != float64(4) || evs[0].Payload["apps"] != float64(4) {
		t.Fatalf("4 apps: %+v", evs)
	}
}

func TestOneAlertPerDay(t *testing.T) {
	h := substest.New(t)
	h.Now = time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	id := link(t, h)
	over := func() {
		for i := range 5 {
			h.FetchAt(id, h.Now, net(i), apps[0])
		}
	}
	over()
	scan(t, h) // 10:00
	for h.Now.Before(time.Date(2026, 10, 8, 9, 45, 0, 0, time.UTC)) {
		h.Advance(15 * time.Minute)
		over()
		scan(t, h)
	}
	if n := len(alertsOf(h)); n != 1 {
		t.Fatalf("before 10:00 the next day: %d alerts", n)
	}
	h.Now = time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	over()
	scan(t, h)
	if n := len(alertsOf(h)); n != 2 {
		t.Errorf("at 10:00 the next day: %d alerts", n)
	}
}

func TestPerLinkOverride(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	if err := h.Mod.Alerts.SetLimits(bg, id, 10, 0, false, "admin"); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		h.FetchAt(id, h.Now, net(i), apps[0])
	}
	scan(t, h)
	if n := len(alertsOf(h)); n != 0 {
		t.Errorf("6 networks under an override of 10: %d alerts", n)
	}
	l, _ := h.Mod.Links.Get(bg, id)
	lim, err := h.Mod.Alerts.Limits(bg, l)
	if err != nil || lim.Networks != 10 || lim.Apps != 3 || lim.Default {
		t.Errorf("limits: %+v %v", lim, err)
	}
}

func TestMutedLinkNoAlert(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	if err := h.Mod.Alerts.SetLimits(bg, id, 0, 0, true, "admin"); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		h.FetchAt(id, h.Now, net(i), apps[i%4])
	}
	scan(t, h)
	if n := len(alertsOf(h)); n != 0 {
		t.Errorf("muted: %d alerts", n)
	}
	c, err := h.Mod.Alerts.Counts(bg, id, h.Now.Add(-alerts.Window))
	if err != nil || len(c.Networks) != 20 || len(c.Apps) != 4 {
		t.Errorf("the counts still show: %d networks, %d apps, %v", len(c.Networks), len(c.Apps), err)
	}
}

func TestWindowIsTwentyFourHours(t *testing.T) {
	h := substest.New(t)
	start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	h.Now = start
	id := link(t, h)
	plan := map[time.Duration]string{ // minutes after midnight → network
		5 * time.Minute: net(1), 10 * time.Minute: net(2), 15 * time.Minute: net(3),
		24*time.Hour + 30*time.Minute: net(4), 24*time.Hour + 45*time.Minute: net(5),
	}
	maxSeen := 0
	for h.Now = start; !h.Now.After(start.Add(25 * time.Hour)); h.Advance(5 * time.Minute) {
		if ip, ok := plan[h.Now.Sub(start)]; ok {
			h.FetchAt(id, h.Now, ip, apps[0])
		}
		if h.Now.Sub(start)%(15*time.Minute) != 0 {
			continue
		}
		scan(t, h)
		c, err := h.Mod.Alerts.Counts(bg, id, h.Now.Add(-alerts.Window))
		if err != nil {
			t.Fatal(err)
		}
		maxSeen = max(maxSeen, len(c.Networks))
	}
	if maxSeen != 3 {
		t.Errorf("at most %d networks in a window, want 3", maxSeen)
	}
	if n := len(alertsOf(h)); n != 0 {
		t.Errorf("alerted: %d", n)
	}
}

func TestDisabledLinkAlerts(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	if err := h.Mod.Links.Disable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		h.FetchAt(id, h.Now, net(i), apps[0])
	}
	scan(t, h)
	if n := len(alertsOf(h)); n != 1 {
		t.Errorf("disabled with 6 networks: %d alerts", n)
	}
}

func TestUnknownAgentsCountByText(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	h.FetchAt(id, h.Now, net(1), "okhttp/4.12")
	h.FetchAt(id, h.Now, net(1), "Dalvik/2.1")
	h.FetchAt(id, h.Now, net(1), "Dalvik/2.1")
	c, err := h.Mod.Alerts.Counts(bg, id, h.Now.Add(-alerts.Window))
	if err != nil || len(c.Apps) != 2 || c.Apps[0].Key != "ua:Dalvik/2.1" || c.Apps[0].Fetches != 2 || c.Apps[1].Key != "ua:okhttp/4.12" {
		t.Errorf("apps: %+v %v", c.Apps, err)
	}
}

func TestQuietLinkNotEvaluated(t *testing.T) {
	h := substest.New(t)
	id := link(t, h)
	for i := range 10 {
		h.FetchAt(id, h.Now.Add(-25*time.Hour), net(i), apps[i%4])
	}
	scan(t, h)
	if n := len(alertsOf(h)); n != 0 {
		t.Errorf("fetches older than 24 h alerted: %d", n)
	}
	var alerted int
	if err := h.App.DB.R.GetContext(bg, &alerted, `SELECT count(*) FROM subs_links WHERE alerted_at IS NOT NULL`); err != nil || alerted != 0 {
		t.Errorf("alerted_at set: %d %v", alerted, err)
	}
}

func TestSharedScanJob(t *testing.T) {
	h := substest.New(t)
	var found bool
	for _, s := range h.Mod.Schedules() {
		if s.Name == alerts.JobSharedScan {
			found = s.Every == 15*time.Minute
		}
	}
	if !found {
		t.Fatal("the shared scan is not scheduled every 15 minutes")
	}
	id := link(t, h)
	for i := range 5 {
		h.FetchAt(id, h.Now, net(i), apps[0])
	}
	h.StartJobs()
	e, err := h.App.Jobs.EnqueueNow(bg, jobs.Request{Type: alerts.JobSharedScan, CreatedBy: "schedule:" + alerts.JobSharedScan})
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if j, err := h.App.Jobs.Job(bg, e.ID); err != nil || j.State != jobs.Succeeded {
		t.Fatalf("job: %+v %v", j, err)
	}
	evs := alertsOf(h)
	if len(evs) != 1 || evs[0].Actor != events.JobActor(e.ID) {
		t.Errorf("alerts: %+v", evs)
	}
}

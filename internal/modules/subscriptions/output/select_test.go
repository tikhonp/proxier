package output_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
)

func TestHideUnhealthyGrace(t *testing.T) {
	h := output.Hide{On: true, States: []string{"blocked", "down"}, Grace: 30 * time.Minute}
	blocked := func(d time.Duration) output.Server {
		s := de1()
		s.Health, s.HealthSince = "blocked", now.Add(-d)
		return s
	}
	served, hidden, all := output.Select([]output.Server{nl1(), blocked(10 * time.Minute)}, h, now)
	if len(served) != 2 || len(hidden) != 0 || all {
		t.Fatalf("10 min: served %d hidden %d", len(served), len(hidden))
	}
	served, hidden, all = output.Select([]output.Server{nl1(), blocked(31 * time.Minute)}, h, now)
	if len(served) != 1 || served[0].Name != "nl-1" || len(hidden) != 1 || hidden[0].Server.Name != "de-1" || hidden[0].For != 31*time.Minute || all {
		t.Fatalf("31 min: served %v hidden %v", served, hidden)
	}
	paused := de1()
	paused.Health, paused.HealthSince = "paused", now.Add(-48*time.Hour)
	withPaused := h
	withPaused.States = append(withPaused.States, "paused")
	if served, _, _ := output.Select([]output.Server{nl1(), paused}, withPaused, now); len(served) != 2 {
		t.Error("a paused server was hidden")
	}
	if served, hidden, _ := output.Select([]output.Server{nl1(), blocked(time.Hour)}, output.Hide{States: h.States, Grace: h.Grace}, now); len(served) != 2 || len(hidden) != 0 {
		t.Error("hide off hid a server")
	}
	// the state must be one of the chosen ones
	degraded := blocked(time.Hour)
	degraded.Health = "degraded"
	if served, _, _ := output.Select([]output.Server{nl1(), degraded}, h, now); len(served) != 2 {
		t.Error("degraded was hidden without being chosen")
	}
}

func TestAllHiddenServesEveryServer(t *testing.T) {
	h := output.Hide{On: true, States: []string{"blocked", "down"}, Grace: 30 * time.Minute}
	a, b := nl1(), de1()
	a.Health, a.HealthSince = "down", now.Add(-time.Hour)
	b.Health, b.HealthSince = "blocked", now.Add(-2*time.Hour)
	served, hidden, all := output.Select([]output.Server{a, b}, h, now)
	if !all || len(served) != 2 || served[0].Name != "nl-1" || len(hidden) != 0 {
		t.Fatalf("served %v hidden %v all %v", served, hidden, all)
	}
	resp := build(t, func() output.Request { r := request(a, b); r.Hide = h; return r }())
	if !resp.AllHidden || len(resp.Lines) != 2 {
		t.Errorf("build: %+v", resp)
	}
	// nothing to hide is not "all hidden"
	if _, _, all := output.Select(nil, h, now); all {
		t.Error("no servers is all hidden")
	}
}

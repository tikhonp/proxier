package refresh_test

import (
	"fmt"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/refresh"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

func set(prefix string, from, to int) snapshot.Set {
	var n []string
	for i := from; i < to; i++ {
		n = append(n, fmt.Sprintf("%s-%d.com", prefix, i))
	}
	return snapshot.New(n, nil, nil)
}

func TestShrinkSettings(t *testing.T) {
	for _, c := range []struct {
		old, new snapshot.Set
		min, pct int
		reason   string
		lostPct  int
	}{
		{set("a", 0, 100), set("a", 0, 65), 20, 30, "shrink", 35},
		{set("a", 0, 100), set("a", 0, 65), 20, 40, "", 35},
		{set("a", 0, 120), set("a", 0, 50), 20, 50, "shrink", 58},
		{set("a", 0, 100), set("a", 0, 50), 20, 50, "", 50}, // exactly half is not more than half
		{set("a", 0, 12), set("a", 0, 3), 20, 50, "", 75},   // under the floor
		{set("a", 0, 10), snapshot.Set{}, 20, 50, "empty", 100},
		// a name changing form isn't lost
		{set("a", 0, 30), snapshot.New(nil, set("a", 0, 30).Suffix, nil), 20, 50, "", 0},
	} {
		reason, lost := refresh.Check(c.old, c.new, c.min, c.pct)
		if reason != c.reason || lost != c.lostPct {
			t.Errorf("%d → %d (min %d, %d %%): %q %d, want %q %d", c.old.Count(), c.new.Count(), c.min, c.pct, reason, lost, c.reason, c.lostPct)
		}
	}

	// the same through the settings
	h := routingtest.New(t)
	h.Up.V2fly("x", names("x", 0, 100))
	id := h.Upstream("v2fly:x")
	if err := h.App.Settings.Set(bg, "admin", "routing", map[string]string{"routing.shrink_pct": "30"}); err != nil {
		t.Fatal(err)
	}
	h.Up.V2fly("x", names("x", 0, 65))
	if out, _ := h.Mod.Refresh.Refresh(bg, id, false, "admin"); out.Result != refresh.Rejected {
		t.Errorf("with 30 %%: %+v", out)
	}
	if err := h.App.Settings.Set(bg, "admin", "routing", map[string]string{"routing.shrink_pct": "40"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := h.Mod.Refresh.Refresh(bg, id, false, "admin"); out.Result != refresh.Accepted {
		t.Errorf("with 40 %%: %+v", out)
	}
}

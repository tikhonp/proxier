package shadowrocket_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestRestoreMakesNewVersion(t *testing.T) {
	h, id := setup(t)
	sr := h.Mod.Shadowrocket
	for i, extra := range []string{"# two\n", "# three\n", "# four\n"} {
		n, changed, err := sr.SaveBase(bg(), id, base+extra, "", "admin")
		if err != nil || !changed || n != i+2 {
			t.Fatalf("save %d: %d %v %v", i+2, n, changed, err)
		}
	}
	n, err := sr.Restore(bg(), id, 2, "admin")
	if err != nil || n != 5 {
		t.Fatalf("restore: %d %v", n, err)
	}
	v, _ := sr.Version(bg(), id, 0)
	if v.Number != 5 || v.Content != base+"# two\n" || v.Note != "restored from v2" {
		t.Errorf("v5: %+v", v)
	}
	vs, _ := sr.Versions(bg(), id)
	if len(vs) != 5 || vs[0].Number != 5 || vs[4].Number != 1 {
		t.Errorf("versions: %+v", vs)
	}
	// unchanged content makes no version and records nothing
	before := len(h.Events("routing.shadowrocket_updated"))
	if n, changed, err := sr.SaveBase(bg(), id, base+"# two\n", "again", "admin"); err != nil || changed || n != 5 {
		t.Errorf("unchanged: %d %v %v", n, changed, err)
	}
	if n, err := sr.Restore(bg(), id, 5, "admin"); err != nil || n != 5 {
		t.Errorf("restore current: %d %v", n, err)
	}
	if after := len(h.Events("routing.shadowrocket_updated")); after != before {
		t.Errorf("events %d → %d", before, after)
	}
	ev := h.Events("routing.shadowrocket_updated")
	if ev[len(ev)-1].Payload["changes"] != "base" || ev[len(ev)-1].Payload["version"] != 5.0 {
		t.Errorf("last event: %+v", ev[len(ev)-1].Payload)
	}
	// a base without [Rule] is refused
	_, _, err = sr.SaveBase(bg(), id, "[General]\n", "", "admin")
	var fe store.FieldErrors
	if !errors.As(err, &fe) || fe["base"] != "shadowrocket.err.no_rule" {
		t.Errorf("no rule: %v", err)
	}
}

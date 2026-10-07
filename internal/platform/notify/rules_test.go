package notify_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/notify"
)

func TestRuleRowsOnlyForNonDefaults(t *testing.T) {
	e := newEnv(t, Options{})
	rows := func() int { return e.count(`SELECT count(*) FROM notification_rules`) }
	changed := func() int {
		return e.count(`SELECT count(*) FROM events WHERE type = 'settings.changed' AND subject_type = 'settings' AND subject_id = 'notifications'`)
	}

	// Already at its default: nothing stored, nothing recorded.
	must(t, e.svc.SetRule(bg, "test.server_down", true))
	if rows() != 0 || changed() != 0 {
		t.Fatalf("a no-op left %d rows and %d events", rows(), changed())
	}

	must(t, e.svc.SetRule(bg, "test.server_down", false))
	must(t, e.svc.SetRule(bg, "test.server_degraded", true))
	if rows() != 2 || changed() != 2 {
		t.Fatalf("%d rows, %d events", rows(), changed())
	}
	evs, _ := events.List(bg, e.h.DB.R, events.Filter{Type: "settings.changed", Limit: 1})
	if keys, _ := evs[0].Payload["keys"].([]any); len(keys) != 1 || keys[0] != "notifications.test.server_degraded" {
		t.Fatalf("payload %v", evs[0].Payload)
	}

	// Back to the default: the row goes, the change is recorded.
	must(t, e.svc.SetRule(bg, "test.server_down", true))
	must(t, e.svc.SetRule(bg, "test.server_degraded", false))
	if rows() != 0 || changed() != 4 {
		t.Fatalf("%d rows, %d events", rows(), changed())
	}

	rules, err := e.svc.Rules(bg)
	must(t, err)
	seen := map[string]notify.Rule{}
	for _, r := range rules {
		seen[r.Type.Name] = r
		if strings.HasPrefix(r.Type.Name, "notification.") {
			t.Fatalf("%s has a rule", r.Type.Name)
		}
	}
	if r := seen["test.server_down"]; !r.Enabled || !r.Default {
		t.Fatalf("%+v", r)
	}
	if r := seen["test.server_degraded"]; r.Enabled || r.Default {
		t.Fatalf("%+v", r)
	}

	if err := e.svc.SetRule(bg, "no.such_type", true); !errors.Is(err, notify.ErrUnknownRule) {
		t.Fatalf("unknown type: %v", err)
	}
	if err := e.svc.SetRule(bg, "notification.failed", true); !errors.Is(err, notify.ErrUnknownRule) {
		t.Fatalf("notification.failed can be turned on: %v", err)
	}
}

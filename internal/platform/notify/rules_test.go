package notify_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
)

var owner = access.PresetOwner.Grant()

func TestRuleRowsOnlyForNonDefaults(t *testing.T) {
	e := newEnv(t, Options{})
	rows := func() int { return e.count(`SELECT count(*) FROM notification_rules WHERE member_id = 1`) }
	changed := func() int {
		return e.count(`SELECT count(*) FROM events WHERE type = 'member.notifications_changed' AND subject_type = 'member' AND subject_id = '1' AND actor = 'member:1' AND json_extract(payload, '$.keys') LIKE 'notifications.%'`)
	}

	// Already at its default: nothing stored, nothing recorded.
	must(t, e.svc.SetRule(bg, 1, owner, "test.server_down", true))
	if rows() != 0 || changed() != 0 {
		t.Fatalf("a no-op left %d rows and %d events", rows(), changed())
	}

	must(t, e.svc.SetRule(bg, 1, owner, "test.server_down", false))
	must(t, e.svc.SetRule(bg, 1, owner, "test.server_degraded", true))
	if rows() != 2 || changed() != 2 {
		t.Fatalf("%d rows, %d events", rows(), changed())
	}
	evs, _ := events.List(bg, e.h.DB.R, events.Filter{Type: "member.notifications_changed", Limit: 1})
	if evs[0].Payload["keys"] != "notifications.test.server_degraded" {
		t.Fatalf("payload %v", evs[0].Payload)
	}

	// Back to the default: the row goes, the change is recorded.
	must(t, e.svc.SetRule(bg, 1, owner, "test.server_down", true))
	must(t, e.svc.SetRule(bg, 1, owner, "test.server_degraded", false))
	if rows() != 0 || changed() != 4 {
		t.Fatalf("%d rows, %d events", rows(), changed())
	}

	rules, err := e.svc.Rules(bg, 1, owner)
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

	if err := e.svc.SetRule(bg, 1, owner, "no.such_type", true); !errors.Is(err, notify.ErrUnknownRule) {
		t.Fatalf("unknown type: %v", err)
	}
	if err := e.svc.SetRule(bg, 1, owner, "notification.failed", true); !errors.Is(err, notify.ErrUnknownRule) {
		t.Fatalf("notification.failed can be turned on: %v", err)
	}
}

// Each member's rules are their own, and a member chooses only the rules of
// what they can be told about.
func TestRulesArePerMemberAndFollowTheGrant(t *testing.T) {
	e := newEnv(t, Options{})
	own := access.Grant{}.With(access.Subscriptions, access.LevelOwn)
	anna := e.join("anna", own, i18n.EN, "")
	must(t, e.svc.SetRule(bg, anna, own, "test.link_shared", false))

	rules, err := e.svc.Rules(bg, 1, owner)
	must(t, err)
	for _, r := range rules {
		if r.Type.Name == "test.link_shared" && !r.Enabled {
			t.Fatal("anna's rule turned member 1's off")
		}
	}

	rules, err = e.svc.Rules(bg, anna, own)
	must(t, err)
	names := map[string]bool{}
	for _, r := range rules {
		names[r.Type.Name] = r.Enabled
	}
	for name, want := range map[string]bool{
		"test.link_shared":      false, // her own links: hers to choose, and off
		"auth.signed_in":        true,  // her own sign-ins
		"auth.password_changed": true,
	} {
		if on, ok := names[name]; !ok || on != want {
			t.Errorf("anna's %s: listed %v, on %v; want on %v", name, ok, on, want)
		}
	}
	for _, name := range []string{"test.server_down", "member.added", "auth.locked"} {
		if _, ok := names[name]; ok {
			t.Errorf("anna, who can't be told about it, has a rule for %s", name)
		}
	}
	if err := e.svc.SetRule(bg, anna, own, "test.server_down", false); !errors.Is(err, notify.ErrUnknownRule) {
		t.Fatalf("a rule out of reach was set: %v", err)
	}
}

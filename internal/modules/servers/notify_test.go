package servers_test

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestActivatedNotificationTexts(t *testing.T) {
	ctx := context.Background()
	// Ready.
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	h.Provisioned()
	ev := h.Events("server.activated")[0]
	loc := h.App.I18n.Localizer(i18n.EN, nil)
	msg, ok, err := h.Mod.RenderNotification(ctx, ev, loc)
	if err != nil || !ok || msg.Title != "🇳🇱 nl-1 is ready (Netherlands 1)" {
		t.Fatalf("ready: %+v %v %v", msg, ok, err)
	}
	if ru, _, _ := h.Mod.RenderNotification(ctx, ev, h.App.I18n.Localizer(i18n.RU, nil)); ru.Title != "🇳🇱 nl-1 готов (Netherlands 1)" {
		t.Errorf("Russian: %q", ru.Title)
	}

	// Forced by Activate anyway.
	h2 := serverstest.NewHarness(t, serverstest.StubProxy())
	h2.StallSmoke(true)
	id := h2.Create(h2.Form())
	h2.Drain()
	if err := h2.Mod.Provision.ActivateAnyway(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	msg, ok, err = h2.Mod.RenderNotification(ctx, h2.Events("server.activated")[0], h2.App.I18n.Localizer(i18n.EN, nil))
	if err != nil || !ok || msg.Title != "🇳🇱 nl-1 is active without a passing proxy test" {
		t.Fatalf("forced: %+v %v %v", msg, ok, err)
	}
	// Other types are left to the default text.
	if _, ok, _ := h2.Mod.RenderNotification(ctx, h2.Events("server.created")[0], loc); ok {
		t.Error("server.created has no custom text")
	}
}

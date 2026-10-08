package routing_test

import (
	"context"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestRefreshNotificationTexts(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	h.Up.V2fly("netflix", "netflix.com\n")
	id := h.Upstream("v2fly:netflix")
	en := h.App.I18n.Localizer(i18n.EN, time.UTC)
	ru := h.App.I18n.Localizer(i18n.RU, time.UTC)
	render := func(loc *i18n.Localizer, e events.Event) (string, string) {
		t.Helper()
		m, ok, err := h.Mod.RenderNotification(ctx, e, loc)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", e.Type, ok, err)
		}
		return m.Title, m.Body
	}
	svc := services.Subject(id)
	for _, c := range []struct {
		e                  events.Event
		enT, enB, ruT, ruB string
	}{
		{events.Event{Type: "routing.snapshot_rejected", Subject: svc, Payload: map[string]any{"reason": "shrink", "old_count": 212.0, "new_count": 83.0, "lost_pct": 61.0, "in_round": false}},
			"netflix: new snapshot held back", "It has 83 domains instead of 212 (a 61 % drop). Targets keep the old list until you decide.",
			"netflix: новый снимок задержан", "В нём 83 домена вместо 212 (потеря 61 %). Цели сохраняют прежний список, пока вы не решите."},
		{events.Event{Type: "routing.snapshot_rejected", Subject: svc, Payload: map[string]any{"reason": "empty", "old_count": 1.0, "new_count": 0.0, "lost_pct": 100.0}},
			"netflix: new snapshot held back", "It has 0 domains instead of 1 (it came back empty). Targets keep the old list until you decide.",
			"netflix: новый снимок задержан", "В нём 0 доменов вместо 1 (он пришёл пустым). Цели сохраняют прежний список, пока вы не решите."},
		{events.Event{Type: "routing.refresh_failing", Subject: svc, Payload: map[string]any{"failures": 3.0, "error": "HTTP 404"}},
			"netflix: refresh failing", "3 failures in a row: HTTP 404. Targets keep the last good list.",
			"netflix: обновление не удаётся", "3 неудачи подряд: HTTP 404. Цели сохраняют последний удачный список."},
		{events.Event{Type: "routing.refresh_digest", Subject: events.Subject{Type: "routing", ID: "refresh"}, Payload: map[string]any{
			"changed": 2.0, "added": 1284.0, "removed": 1.0, "rejected": 1.0, "failing": 1.0, "still_failing": 0.0}},
			"Routing refresh: 2 services changed (+1,284 / −1 domains)", "1 held back · 1 started failing",
			"Обновление маршрутизации: изменено 2 сервиса (+1\u00a0284 / −1 доменов)", "1 задержан · 1 начал падать"},
		{events.Event{Type: "routing.refresh_digest", Subject: events.Subject{Type: "routing", ID: "refresh"}, Payload: map[string]any{
			"changed": 1.0, "added": 3.0, "removed": 0.0}},
			"Routing refresh: 1 service changed (+3 / −0 domains)", "",
			"Обновление маршрутизации: изменён 1 сервис (+3 / −0 доменов)", ""},
		{events.Event{Type: "routing.refresh_digest", Subject: events.Subject{Type: "routing", ID: "refresh"}, Payload: map[string]any{
			"changed": 0.0, "rejected": 5.0, "still_failing": 2.0}},
			"Routing refresh: no changes", "5 held back · 2 still failing",
			"Обновление маршрутизации: без изменений", "5 задержано · 2 всё ещё падают"},
		{events.Event{Type: "routing.catalog_refresh_failed", Subject: events.Subject{Type: "routing", ID: "catalog"}, Payload: map[string]any{
			"source": "v2fly", "error": "HTTP 502", "since": "2026-10-06T04:30:00.000Z"}},
			"Catalog: v2fly failing since 6 Oct", "HTTP 502. Search has no catalog of it yet.",
			"Каталог: v2fly не обновляется с 6 окт", "HTTP 502. В поиске его каталога ещё нет."},
	} {
		if title, body := render(en, c.e); title != c.enT || body != c.enB {
			t.Errorf("EN %s:\n%q\n%q", c.e.Type, title, body)
		}
		if title, body := render(ru, c.e); title != c.ruT || body != c.ruB {
			t.Errorf("RU %s:\n%q\n%q", c.e.Type, title, body)
		}
	}

	// with a catalog of its own, the failing source's date
	h.Up.V2fly("apple", "apple.com\n")
	h.StartJobs()
	if _, err := h.Mod.Catalog.RefreshNow(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	_, body := render(en, events.Event{Type: "routing.catalog_refresh_failed", Subject: events.Subject{Type: "routing", ID: "catalog"},
		Payload: map[string]any{"source": "v2fly", "error": "HTTP 502", "since": "2026-10-09T04:30:00.000Z"}})
	if body != "HTTP 502. Search shows its catalog of 8 Oct." {
		t.Errorf("catalog body: %q", body)
	}
	// the router texts stay the platform's defaults
	if _, ok, _ := h.Mod.RenderNotification(ctx, events.Event{Type: "routing.router_sync_failed"}, en); ok {
		t.Error("the module rendered a router notification")
	}
}

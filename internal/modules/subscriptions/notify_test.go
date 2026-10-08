package subscriptions_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestNotificationTexts(t *testing.T) {
	ctx := context.Background()
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	moscow, _ := time.LoadLocation("Europe/Moscow")
	// 01:30 on 10 Oct in Moscow is still 9 Oct in UTC
	expires := time.Date(2026, 10, 10, 1, 30, 0, 0, moscow)
	id, err := h.Mod.Links.Create(ctx, links.New{Name: "Mom", SubscriptionID: sub, Expires: expires, Lang: i18n.EN}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	en, ru, utc := h.App.I18n.Localizer(i18n.EN, moscow), h.App.I18n.Localizer(i18n.RU, moscow), h.App.I18n.Localizer(i18n.EN, time.UTC)

	if err := h.Mod.Links.ExpiryStep(ctx, h.Now, "job:1"); err != nil {
		t.Fatal(err)
	}
	soon := h.Events("link.expiring_soon")[0]
	for loc, want := range map[*i18n.Localizer][2]string{
		en:  {"Mom expires on 10 Oct", "Extend it on its page if it should keep working."},
		ru:  {"Срок ссылки Mom истекает 10 окт", "Продлите его на странице ссылки, если она должна работать дальше."},
		utc: {"Mom expires on 9 Oct", "Extend it on its page if it should keep working."},
	} {
		msg, ok, err := h.Mod.RenderNotification(ctx, soon, loc)
		if err != nil || !ok || msg.Title != want[0] || msg.Body != want[1] {
			t.Errorf("expiring_soon: %+v %v %v, want %q", msg, ok, err, want)
		}
	}

	h.Advance(3 * 24 * time.Hour)
	if err := h.Mod.Links.ExpiryStep(ctx, h.Now, "job:1"); err != nil {
		t.Fatal(err)
	}
	expired := h.Events("link.expired")[0]
	for loc, want := range map[*i18n.Localizer][2]string{
		en: {"Mom expired on 10 Oct", "Its app gets the “expired” entry on its next refresh."},
		ru: {"Срок ссылки Mom истёк 10 окт", "Приложение получит заглушку «Срок истёк» при следующем обновлении."},
	} {
		msg, ok, err := h.Mod.RenderNotification(ctx, expired, loc)
		if err != nil || !ok || msg.Title != want[0] || msg.Body != want[1] {
			t.Errorf("expired: %+v %v %v, want %q", msg, ok, err, want)
		}
	}
	_ = id

	// a date-only expiry names its last day, like the stub entry
	dateOnly, err := links.ParseExpiry("on", "2026-12-01", "", moscow, h.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.SetExpiry(ctx, id, dateOnly, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Now = dateOnly.Add(-24 * time.Hour)
	if err := h.Mod.Links.ExpiryStep(ctx, h.Now, "job:1"); err != nil {
		t.Fatal(err)
	}
	evs := h.Events("link.expiring_soon")
	if msg, _, _ := h.Mod.RenderNotification(ctx, evs[len(evs)-1], en); msg.Title != "Mom expires on 1 Dec" {
		t.Errorf("date only: %q", msg.Title)
	}

	// link.shared_suspected: counts in the plural forms, never an IP
	alex, _ := h.Link(sub, "Alex")
	for i := range 6 {
		h.FetchAt(alex, h.Now, fmt.Sprintf("198.51.%d.7", i), "Happ/1.6")
	}
	if err := h.Mod.Alerts.Scan(ctx, h.Now, "job:2"); err != nil {
		t.Fatal(err)
	}
	e := h.Events("link.shared_suspected")[0]
	for loc, want := range map[*i18n.Localizer][2]string{
		en: {"Alex looks shared", "Fetched from 6 networks and 1 app in 24 h."},
		ru: {"Ссылкой Alex, похоже, поделились", "За сутки её запросили из 6 сетей и 1 приложения."},
	} {
		msg, ok, err := h.Mod.RenderNotification(ctx, e, loc)
		if err != nil || !ok || msg.Title != want[0] || msg.Body != want[1] {
			t.Errorf("shared: %+v %v %v, want %q", msg, ok, err, want)
		}
		if strings.Contains(msg.Title+msg.Body+fmt.Sprint(e.Payload), "198.51.") {
			t.Error("an IP in the notification")
		}
	}
	if got := en.T("notify.subscription.all_unhealthy.body", i18n.Args{"count": 2}); got != "Hiding them would leave nothing, so all 2 are served." {
		t.Errorf("all_unhealthy: %q", got)
	}
}

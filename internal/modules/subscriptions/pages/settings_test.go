package pages_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func TestSubscriptionsSettingsPage(t *testing.T) {
	h := substest.New(t)
	get := func(key string) string {
		v, _ := h.App.Settings.Get(t.Context(), key)
		return v
	}
	body := page(t, h, "/settings/subscriptions")
	mustContain(t, body, `href="/settings/subscriptions"`, "Language of new links", "https://ipinfo.io/{ip}/country",
		"never the person&#39;s own address", `value="72h0m0s"`, `value="4"`, `value="2160h0m0s"`)
	noInline(t, body)

	for value, msg := range map[string]string{
		"ftp://geo.example.com/{ip}":      "must be an http:// or https:// address",
		"https://geo.example.com/country": "must contain {ip}",
	} {
		rec := h.Login.Post("/settings/subscriptions", url.Values{"subscriptions.network_country_url": {value}, "subscriptions.alert_networks": {"6"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", value, rec.Code)
		}
		mustContain(t, rec.Body.String(), msg, `value="`+value+`"`)
		if get("subscriptions.alert_networks") != "4" {
			t.Error("a refused save changed a value")
		}
	}

	rec := h.Login.Post("/settings/subscriptions", url.Values{
		"subscriptions.network_country_url": {""}, "subscriptions.alert_networks": {"6"}, "subscriptions.link_language": {"en"},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/subscriptions?saved=1" {
		t.Fatalf("save: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if get("subscriptions.network_country_url") != "" || get("subscriptions.alert_networks") != "6" || get("subscriptions.link_language") != "en" {
		t.Error("not saved")
	}
	mustContain(t, page(t, h, "/settings/subscriptions?saved=1"), "Saved")
	evs := h.Events("settings.changed")
	if last := evs[len(evs)-1]; last.Subject.String() != "settings:subscriptions" {
		t.Errorf("settings.changed: %+v", last)
	}
}

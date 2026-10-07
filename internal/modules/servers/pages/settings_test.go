package pages_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestServersSettingsZoneWarning(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	l := s.SignIn("")
	set := func(section string, kv map[string]string) {
		t.Helper()
		if err := s.App.Settings.Set(t.Context(), "admin", section, kv); err != nil {
			t.Fatal(err)
		}
	}
	const warning = "is in no zone allowed for Cloudflare"

	// no zone allowed yet: the default pattern warns, and says which host it makes
	body := l.Get("/settings/servers").Body.String()
	if !strings.Contains(body, "xx-1.hosts.tikhonnnnn.com") || !strings.Contains(body, warning) || !strings.Contains(body, `href="/settings/integrations/cloudflare"`) {
		t.Errorf("no zone allowed:\n%s", body)
	}
	// the side menu lists the page
	if !strings.Contains(body, `href="/settings/servers"`) {
		t.Errorf("Settings → Servers is missing from the menu")
	}

	// the zone is allowed: no warning
	set("cloudflare", map[string]string{"cloudflare.zones": "tikhonnnnn.com"})
	if body := l.Get("/settings/servers").Body.String(); strings.Contains(body, warning) {
		t.Errorf("zone allowed but warned:\n%s", body)
	}

	// a pattern in another domain warns again
	set("servers", map[string]string{"servers.hostname_pattern": "{location}-{number}.px.example.org"})
	if body := l.Get("/settings/servers").Body.String(); !strings.Contains(body, "xx-1.px.example.org") || !strings.Contains(body, warning) {
		t.Errorf("another domain:\n%s", body)
	}
	// ...until that zone is allowed too (longest suffix wins, any allowed zone counts)
	set("cloudflare", map[string]string{"cloudflare.zones": "tikhonnnnn.com,example.org"})
	if body := l.Get("/settings/servers").Body.String(); strings.Contains(body, warning) {
		t.Errorf("both zones allowed but warned")
	}
}

func TestServersSettingsSave(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	l := s.SignIn("")
	get := func(key string) string {
		v, _ := s.App.Settings.Get(t.Context(), key)
		return v
	}

	body := l.Get("/settings/servers").Body.String()
	for _, want := range []string{"https://ipinfo.io/{ip}/country", "https://speed.cloudflare.com/__down?bytes=262144", `value="15s"`, `value="5s"`} {
		if !strings.Contains(body, want) {
			t.Errorf("defaults: page lacks %q", want)
		}
	}

	// every refusal is shown beside its field and nothing is saved
	rec := l.Post("/settings/servers", url.Values{
		"servers.hostname_pattern":   {"{location}-{number}.hosts.tikhonnnnn.com"},
		"servers.ip_country_url":     {"http://ipinfo.io/{ip}/country"},
		"servers.proxy_test_url":     {"ftp://files.example.com/x"},
		"servers.proxy_test_timeout": {"1s"},
		"servers.proxy_test_stall":   {"2m"},
	})
	body = rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad values: %d", rec.Code)
	}
	for _, want := range []string{"must be an https:// address", "must be an http:// or https:// address", "must be from 5s to 2m0s", "must be from 1s to 1m0s", `value="http://ipinfo.io/{ip}/country"`} {
		if !strings.Contains(body, want) {
			t.Errorf("errors: page lacks %q", want)
		}
	}
	if get("servers.proxy_test_timeout") != "15s" {
		t.Error("a refused save changed a value")
	}
	if rec := l.Post("/settings/servers", url.Values{"servers.ip_country_url": {"https://geo.example.com/country"}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "must contain {ip}") {
		t.Errorf("no {ip}: %d", rec.Code)
	}

	// good values, and an empty country URL (off)
	rec = l.Post("/settings/servers", url.Values{
		"servers.ip_country_url":     {""},
		"servers.proxy_test_url":     {"https://files.example.com/100k.bin"},
		"servers.proxy_test_timeout": {"20s"},
		"servers.proxy_test_stall":   {"3s"},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/servers?saved=1" {
		t.Fatalf("good save: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if get("servers.ip_country_url") != "" || get("servers.proxy_test_url") != "https://files.example.com/100k.bin" ||
		get("servers.proxy_test_timeout") != "20s" || get("servers.proxy_test_stall") != "3s" {
		t.Error("the values were not saved")
	}
	if body := l.Get("/settings/servers?saved=1").Body.String(); !strings.Contains(body, "Saved") {
		t.Errorf("no confirmation:\n%s", body)
	}
}

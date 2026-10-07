package pages_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare/cloudflaretest"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

const cfToken = "cf-page-TOKEN-0123456789"

// cloudflareSite is a signed-in site whose Cloudflare is the fake API.
func cloudflareSite(t *testing.T) (*sitetest.Site, *sitetest.Login, *cloudflaretest.Fake) {
	t.Helper()
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	f := cloudflaretest.New(t, cfToken, "tikhonnnnn.com", "example.org")
	mod, _ := s.App.Module("servers")
	mod.(*servers.Module).CloudflareClient = f.ClientFor
	return s, s.SignIn(""), f
}

func TestCloudflareSettingsPage(t *testing.T) {
	s, l, f := cloudflareSite(t)
	get := func(key string) string {
		v, err := s.App.Settings.Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// before anything: the row says so, the page asks for a token, no zones to tick
	if body := l.Get("/settings/integrations").Body.String(); !strings.Contains(body, "Cloudflare") || !strings.Contains(body, "not configured") {
		t.Errorf("integrations list:\n%s", body)
	}
	body := l.Get("/settings/integrations/cloudflare").Body.String()
	if !strings.Contains(body, `name="token"`) || strings.Contains(body, `name="zone"`) {
		t.Errorf("fresh page:\n%s", body)
	}

	// a refused token saves nothing and is not echoed
	rec := l.Post("/settings/integrations/cloudflare/token", url.Values{"token": {"cf-WRONG-token"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Cloudflare refused this token.") || strings.Contains(rec.Body.String(), "WRONG-token") {
		t.Fatalf("refused token: %d\n%s", rec.Code, rec.Body)
	}
	if get("cloudflare.api_token") != "" {
		t.Error("a refused token was saved")
	}
	if rec := l.Post("/settings/integrations/cloudflare/token", url.Values{"token": {" "}}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Paste an API token.") {
		t.Errorf("empty token: %d", rec.Code)
	}

	// Cloudflare being unreachable is not "refused", and saves nothing either
	f2 := cloudflaretest.New(t, cfToken, "tikhonnnnn.com")
	f2.Fail("GET /user/tokens/verify", 500, 500, 500, 500)
	mod, _ := s.App.Module("servers")
	mod.(*servers.Module).CloudflareClient = f2.ClientFor
	rec = l.Post("/settings/integrations/cloudflare/token", url.Values{"token": {cfToken}})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Cloudflare could not be reached.") || get("cloudflare.api_token") != "" {
		t.Errorf("unreachable: %d\n%s", rec.Code, rec.Body)
	}
	mod.(*servers.Module).CloudflareClient = f.ClientFor

	// a good token is saved and lists the zones, none ticked
	rec = l.Post("/settings/integrations/cloudflare/token", url.Values{"token": {cfToken}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/integrations/cloudflare?saved=token" {
		t.Fatalf("good token: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if get("cloudflare.api_token") != cfToken {
		t.Error("the token was not saved")
	}
	body = l.Get("/settings/integrations/cloudflare?saved=token").Body.String()
	for _, want := range []string{"Token saved.", `value="tikhonnnnn.com"`, `value="example.org"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page after the token lacks %q", want)
		}
	}
	if strings.Contains(body, "checked") || strings.Contains(body, cfToken) {
		t.Errorf("nothing should be ticked, and the token never shows:\n%s", body)
	}

	// ticking saves cloudflare.zones; a name the token does not see is dropped
	rec = l.Post("/settings/integrations/cloudflare/zones", url.Values{"zone": {"tikhonnnnn.com", "not-mine.net"}})
	if rec.Code != http.StatusSeeOther || get("cloudflare.zones") != "tikhonnnnn.com" {
		t.Fatalf("zones: %d, saved %q", rec.Code, get("cloudflare.zones"))
	}
	body = l.Get("/settings/integrations/cloudflare?saved=zones").Body.String()
	if !strings.Contains(body, `value="tikhonnnnn.com" checked`) || strings.Contains(body, `value="example.org" checked`) || !strings.Contains(body, "Zones saved.") {
		t.Errorf("after ticking:\n%s", body)
	}
	if body := l.Get("/settings/integrations").Body.String(); !strings.Contains(body, "Token saved · allowed zones: 1") {
		t.Errorf("integrations list after setup:\n%s", body)
	}

	// Test lists the zones again
	if body := l.Post("/settings/integrations/cloudflare/test", nil).Body.String(); !strings.Contains(body, "The token works. Zones it sees: 2.") {
		t.Errorf("test:\n%s", body)
	}
	// the token being revoked later shows on the page and in Test
	f.RejectToken()
	if body := l.Get("/settings/integrations/cloudflare").Body.String(); !strings.Contains(body, "Cloudflare refused this token.") {
		t.Errorf("revoked token on the page:\n%s", body)
	}
	if body := l.Post("/settings/integrations/cloudflare/test", nil).Body.String(); !strings.Contains(body, "Cloudflare refused this token.") {
		t.Errorf("revoked token in Test:\n%s", body)
	}
	// ...and saving zones then changes nothing
	if rec := l.Post("/settings/integrations/cloudflare/zones", url.Values{"zone": {"example.org"}}); rec.Code == http.StatusSeeOther || get("cloudflare.zones") != "tikhonnnnn.com" {
		t.Errorf("zones saved with a dead token: %d, %q", rec.Code, get("cloudflare.zones"))
	}

	// the page is behind the sign-in, and the driver reads what was saved
	if rec := s.Do(sitetest.Req{Path: "/settings/integrations/cloudflare"}); rec.Code == http.StatusOK {
		t.Errorf("signed out: %d", rec.Code)
	}
	if z, ok, _ := mod.(*servers.Module).DNS.Covers(t.Context(), "nl-1.hosts.tikhonnnnn.com"); !ok || z != "tikhonnnnn.com" {
		t.Errorf("the driver does not see the saved zones: %q %v", z, ok)
	}
	var _ = cloudflare.ErrTokenRejected
}

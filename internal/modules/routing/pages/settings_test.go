package pages_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestRoutingSettingsPage(t *testing.T) {
	h := routingtest.New(t)
	body := h.Login.Get("/settings/routing").Body.String()
	has(t, "page", body, "Upstream refresh", "Catalog", "Routers", "Used from router sync on.", `href="/settings/routing"`,
		`name="routing.refresh_at" class="pc-inp" value="04:00"`, `name="routing.shrink_min"`, `name="routing.shrink_pct"`,
		`name="routing.catalog_at"`, `name="routing.github_token" type="password"`, `name="routing.sync_delay"`,
		`name="routing.drift_every"`, `name="routing.drift_repair"`, "Daily refresh at", "Hold back a loss over, %")
	if i, j := strings.Index(body, "Upstream refresh"), strings.Index(body, "Routers"); i < 0 || j < i {
		t.Error("the groups' order")
	}
	if strings.Contains(body, "clear.routing.github_token") {
		t.Error("a clear box for a token that isn't set")
	}
	if !strings.Contains(h.Login.Get("/settings/general").Body.String(), `href="/settings/routing"`) {
		t.Error("Settings has no Routing in its menu")
	}

	form := url.Values{"routing.refresh_at": {"03:15"}, "routing.shrink_pct": {"40"}, "routing.github_token": {"ghp_abc"},
		"routing.drift_repair": {"false"}}
	rec := h.Login.Post("/settings/routing", form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/routing?saved=1" {
		t.Fatalf("save: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	for k, want := range map[string]string{"routing.refresh_at": "03:15", "routing.shrink_pct": "40", "routing.github_token": "ghp_abc",
		"routing.drift_repair": "false", "routing.catalog_at": "04:30"} {
		if v, _ := h.App.Settings.Get(bg(), k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	body = h.Login.Get("/settings/routing?saved=1").Body.String()
	has(t, "saved", body, "Saved.", `value="03:15"`, "clear.routing.github_token", "leave empty to keep")
	if strings.Contains(body, "ghp_abc") {
		t.Error("the token is on the page")
	}
	// an empty token field keeps the token; the clear box removes it
	h.Login.Post("/settings/routing", url.Values{"routing.github_token": {""}, "routing.refresh_at": {"03:15"}})
	if v, _ := h.App.Settings.Get(bg(), "routing.github_token"); v != "ghp_abc" {
		t.Errorf("an empty field changed the token: %q", v)
	}
	h.Login.Post("/settings/routing", url.Values{"routing.github_token": {""}, "clear.routing.github_token": {"1"}})
	if v, _ := h.App.Settings.Get(bg(), "routing.github_token"); v != "" {
		t.Errorf("clear kept the token: %q", v)
	}
	// a refused value writes nothing and shows the error
	rec = h.Login.Post("/settings/routing", url.Values{"routing.refresh_at": {"25:00"}, "routing.shrink_pct": {"20"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "must be a time of day as HH:MM") ||
		!strings.Contains(rec.Body.String(), `value="25:00"`) {
		t.Errorf("refused: %d", rec.Code)
	}
	if v, _ := h.App.Settings.Get(bg(), "routing.shrink_pct"); v != "40" {
		t.Errorf("a refused save wrote shrink_pct = %q", v)
	}
}

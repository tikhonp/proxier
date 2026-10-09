package routing_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery/discoverytest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestChromiumIntegrationRow(t *testing.T) {
	h := routingtest.New(t)
	ctx := i18n.WithLocalizer(context.Background(), h.App.I18n.Localizer(i18n.EN, time.UTC))
	if rows := h.Mod.Integrations(ctx); len(rows) != 1 || rows[0].On || rows[0].State != "not configured: set PROXIER_CHROMIUM_URL" ||
		rows[0].Name != "Chromium (discovery)" || rows[0].Href != "/routing/discover" {
		t.Fatalf("not configured: %+v", rows)
	}

	b := discoverytest.New()
	h = routingtest.New(t, routingtest.WithBrowser(b))
	ctx = i18n.WithLocalizer(context.Background(), h.App.I18n.Localizer(i18n.EN, time.UTC))
	if rows := h.Mod.Integrations(ctx); !rows[0].On || rows[0].State != "connected · HeadlessChrome/131.0.6778.85" {
		t.Fatalf("connected: %+v", rows)
	}
	page := h.Login.Get("/settings/integrations").Body.String()
	if !strings.Contains(page, "Chromium (discovery)") || !strings.Contains(page, "connected · HeadlessChrome/131") {
		t.Error("Settings → Integrations has no Chromium row")
	}
	b.Fail(discoverytest.ErrUnreachable)
	if rows := h.Mod.Integrations(ctx); rows[0].On || !strings.HasPrefix(rows[0].State, "unreachable: cdp: dial tcp") {
		t.Fatalf("unreachable: %+v", rows)
	}
}

func TestDiscoverySubjectNamed(t *testing.T) {
	h := routingtest.New(t)
	ctx := i18n.WithLocalizer(context.Background(), h.App.I18n.Localizer(i18n.EN, time.UTC))
	id, err := h.Mod.Discovery.Start(ctx, discovery.Start{Website: "https://www.example.com/x", Via: discovery.ViaDirect}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.Mod.NameSubjects(ctx, "discovery", []string{strconv.FormatInt(id, 10), "99"})
	if err != nil || len(got) != 1 || got["1"].Label != "run #1 · www.example.com" || got["1"].Href != "/routing/discover/1" {
		t.Errorf("subjects: %+v %v", got, err)
	}
}

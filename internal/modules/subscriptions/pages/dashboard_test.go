package pages_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func TestLinksDashboardArea(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Friends", 1)
	alex, _ := h.Link(sub, "Alex")
	masha, _ := h.Link(sub, "Masha — Pixel")
	later, _ := h.Link(sub, "Later")
	if err := h.Mod.Links.SetExpiry(t.Context(), later, h.Now.Add(30*24*time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	if body := page(t, h, "/"); strings.Contains(body, "Links →") {
		t.Fatal("an area with nothing to show")
	}

	if err := h.Mod.Links.SetExpiry(t.Context(), masha, h.Now.Add(4*24*time.Hour+time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	shared(t, h, alex, 6)
	body := page(t, h, "/")
	mustContain(t, body, "Links →", ">Alex<", "alert", "6 networks, 4 apps in 24 h", ">Masha — Pixel<", "expiring", "in 4 days")
	mustNotContain(t, body, ">Later<")
	if strings.Index(body, ">Alex<") > strings.Index(body, ">Masha — Pixel<") {
		t.Error("alerts come first")
	}
	noInline(t, body)

	// expired links and muted alerts leave the area
	if err := h.Mod.Alerts.SetLimits(t.Context(), alex, 0, 0, true, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Advance(5 * 24 * time.Hour)
	if body := page(t, h, "/"); strings.Contains(body, "Links →") {
		t.Error("the area stayed")
	}
}

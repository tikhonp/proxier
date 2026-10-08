package pages_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

var alertApps = []string{"Happ/1.6.1", "v2RayTun/4.0", "HiddifyNext/2.5", "Shadowrocket/2.2"}

// shared gives the link fetches from n networks (the i-th in 198.51.i.0/24)
// and the four apps, then runs the shared-link scan.
func shared(t *testing.T, h *substest.Harness, id int64, n int) {
	t.Helper()
	for i := range n {
		h.FetchAt(id, h.Now.Add(-time.Minute), fmt.Sprintf("198.51.%d.7", i), alertApps[i%len(alertApps)])
	}
	if err := h.Mod.Alerts.Scan(t.Context(), h.Now, "job:1"); err != nil {
		t.Fatal(err)
	}
}

func TestLinkAlertPanels(t *testing.T) {
	h := substest.New(t)
	id, _ := h.Link(h.Subscription("Friends", 1), "Alex")
	h.Exec(`INSERT INTO subs_network_countries (network, country, looked_up_at) VALUES ('198.51.0.0/24', 'NL', ?)`, h.Now.UTC().Format("2006-01-02T15:04:05.000Z"))
	h.FetchAt(id, h.Now.Add(-3*24*time.Hour), "203.0.113.9", "okhttp/4.12") // only in the 7 days
	shared(t, h, id, 6)
	href := fmt.Sprintf("/links/%d", id)

	body := page(t, h, href)
	mustContain(t, body, "Shared-link alert", "looks shared",
		"Fetched from 6 networks and 4 apps in the last 24 h. The limits are more than 4 networks or more than 3 apps.",
		"One person&#39;s devices rarely explain that.", "Regenerate token…", "Disable…", "Raise limits", "Mute alerts",
		`href="`+href+`/alerts"`, `name="action" value="mute"`, "No more alerts for this link.",
		"Alert limits", "defaults · more than 4 networks or 3 apps in 24 h",
		"Who fetches it", "Networks · 6", "Apps · 4", "198.51.0.0/24", "🇳🇱 NL", ">Hiddify<", `href="`+href+`?window=7d"`)
	mustNotContain(t, body, "203.0.113.0/24", "Networks · 7")
	noInline(t, body)

	// 7 days: the older network and its unknown app too; the band still counts 24 h
	week := page(t, h, href+"?window=7d")
	mustContain(t, week, "Networks · 7", "Apps · 5", "203.0.113.0/24", "Other: okhttp/4.12",
		"Fetched from 6 networks and 4 apps in the last 24 h.")

	// the band's counts are the ones of now
	h.Advance(10 * time.Minute)
	h.FetchAt(id, h.Now, "198.51.50.7", alertApps[0])
	mustContain(t, page(t, h, href), "Fetched from 7 networks and 4 apps in the last 24 h.")

	// muted: no band, no marker, the counts still show
	if err := h.Mod.Alerts.SetLimits(t.Context(), id, 0, 0, true, "admin"); err != nil {
		t.Fatal(err)
	}
	body = page(t, h, href)
	mustNotContain(t, body, "Shared-link alert", "looks shared")
	mustContain(t, body, "Who fetches it", "Networks · 7", "Alert limits", "muted")

	// a day after the alert, unmuted: the alert has passed
	if err := h.Mod.Alerts.SetLimits(t.Context(), id, 0, 0, false, "admin"); err != nil {
		t.Fatal(err)
	}
	mustContain(t, page(t, h, href), "Shared-link alert")
	h.Advance(24 * time.Hour)
	mustNotContain(t, page(t, h, href), "Shared-link alert")
}

func TestAlertLimitsPage(t *testing.T) {
	h := substest.New(t)
	id, _ := h.Link(h.Subscription("Friends", 1), "Alex")
	shared(t, h, id, 5)
	href := fmt.Sprintf("/links/%d", id)

	body := page(t, h, href+"/alerts")
	mustContain(t, body, "Alert limits of Alex", `name="networks"`, `placeholder="4"`, `placeholder="3"`, "Mute alerts for this link")
	noInline(t, body)

	// refused inline, nothing saved
	rec := h.Login.Post(href+"/alerts", url.Values{"networks": {"abc"}, "apps": {"0"}})
	if rec.Code != http.StatusUnprocessableEntity || strings.Count(rec.Body.String(), "A whole number from 1 to 1000") != 2 {
		t.Fatalf("bad limits: %d", rec.Code)
	}
	if n := len(h.Events("link.changed")); n != 0 {
		t.Fatalf("a refused save recorded %d events", n)
	}

	rec = h.Login.Post(href+"/alerts", url.Values{"networks": {"10"}, "apps": {""}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != href {
		t.Fatalf("save: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	l, _ := h.Mod.Links.Get(t.Context(), id)
	if l.AlertNetworks != 10 || l.AlertApps != 0 || l.AlertsMuted {
		t.Errorf("saved: %+v", l)
	}
	evs := h.Events("link.changed")
	if len(evs) != 1 || evs[0].Payload["fields"] != "alert limits" {
		t.Fatalf("events: %+v", evs)
	}
	// raising the limits leaves the alert
	if l.AlertedAt.IsZero() || !strings.Contains(page(t, h, href), "Shared-link alert") {
		t.Error("raising the limits cleared the alert")
	}
	mustContain(t, page(t, h, href), "this link&#39;s · more than 10 networks or 3 apps in 24 h")
	// the same values again: nothing recorded
	h.Login.Post(href+"/alerts", url.Values{"networks": {"10"}, "apps": {""}})
	if n := len(h.Events("link.changed")); n != 1 {
		t.Errorf("an unchanged save recorded: %d", n)
	}

	// Mute alerts from the band keeps the limits
	h.Login.Post(href+"/alerts", url.Values{"action": {"mute"}})
	l, _ = h.Mod.Links.Get(t.Context(), id)
	if !l.AlertsMuted || l.AlertNetworks != 10 {
		t.Errorf("mute: %+v", l)
	}
	mustContain(t, page(t, h, href+"/alerts"), "Alerts are muted for this link", "Unmute")
	h.Login.Post(href+"/alerts", url.Values{"action": {"unmute"}})
	l, _ = h.Mod.Links.Get(t.Context(), id)
	if l.AlertsMuted || l.AlertNetworks != 10 {
		t.Errorf("unmute: %+v", l)
	}
	evs = h.Events("link.changed")
	if len(evs) != 3 || evs[1].Payload["fields"] != "alerts muted" || evs[2].Payload["fields"] != "alerts muted" {
		t.Errorf("mute events: %+v", evs)
	}

	// a deleted link's limits can't change
	if err := h.Mod.Links.Delete(t.Context(), id, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := h.Login.Get(href + "/alerts"); rec.Code != http.StatusConflict {
		t.Errorf("deleted, page: %d", rec.Code)
	}
	if rec := h.Login.Post(href+"/alerts", url.Values{"action": {"mute"}}); rec.Code != http.StatusConflict {
		t.Errorf("deleted, post: %d", rec.Code)
	}
}

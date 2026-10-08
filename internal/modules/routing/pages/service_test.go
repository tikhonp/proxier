package pages_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func nfNames(from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "nf-%d.com\n", i)
	}
	return b.String()
}

func TestRefreshAndRejectionOnServicePage(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", nfNames(0, 212))
	id := h.Upstream("v2fly:netflix")
	h.List("Main", id)
	h.List("Parents", id)
	href := fmt.Sprintf("/routing/services/%d", id)

	body := h.Login.Get(href).Body.String()
	has(t, "page", body, ">Refresh now<", `action="`+href+`/refresh"`, `data-key="r"`, `id="source-area"`,
		"Not refreshed yet", ">ok<")
	if strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Error("the Source area polls with no refresh running")
	}

	// Refresh now: queued, the band, the Source area polls
	rec := h.Login.Post(href+"/refresh", url.Values{})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), href+"?refresh=") {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body = h.Login.Get(rec.Header().Get("Location")).Body.String()
	has(t, "queued", body, "Refresh queued · job #", `hx-get="`+href+`/source?polling=1"`, `hx-trigger="every 2s"`)
	src := h.Site.Do(sitetest.Req{Method: "GET", Path: href + "/source?polling=1", Cookies: []*http.Cookie{h.Login.Cookie},
		Header: http.Header{"Hx-Request": {"true"}}})
	if !strings.Contains(src.Body.String(), `hx-trigger="every 2s"`) || src.Header().Get("HX-Refresh") != "" {
		t.Error("the Source area stopped polling while the job is queued")
	}

	// the job runs: the snapshot comes back held back; the poll stops and reloads
	h.Up.V2fly("netflix", nfNames(131, 214))
	h.StartJobs()
	h.Drain()
	src = h.Site.Do(sitetest.Req{Method: "GET", Path: href + "/source?polling=1", Cookies: []*http.Cookie{h.Login.Cookie},
		Header: http.Header{"Hx-Request": {"true"}}})
	if strings.Contains(src.Body.String(), "hx-trigger") || src.Header().Get("HX-Refresh") != "true" {
		t.Errorf("after the job: %s %q", src.Body.String(), src.Header().Get("HX-Refresh"))
	}
	has(t, "source after", src.Body.String(), "Last check 2026-10-08 15:00 · fetched ok")

	body = h.Login.Get(href).Body.String()
	has(t, "band", body, "Today’s snapshot was held back", "It has 83 domains instead of 212: a 61 % drop, above the 50 % safety limit.",
		"Removed · 131", "Added · 2", "nf-0.com", "nf-212.com", "… 111 more", "Keep 212, dismiss this one", ">Accept anyway…<",
		"Accept 83 names for netflix?", "Targets of Main and Parents lose 131 names on their next sync.",
		"Routers keep the 212 domains until you decide.", ">waiting<", "/snapshots/")
	if strings.Contains(body, "Refresh queued") {
		t.Error("the queued band outlived the job")
	}

	w, _, _ := h.Mod.Refresh.Waiting(bg(), id)
	snap := fmt.Sprintf("%s/snapshots/%d", href, w.ID)
	// Dismiss
	rec = h.Login.Post(snap+"/dismiss", url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != href+"?dismissed=1" {
		t.Fatalf("dismiss: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body = h.Login.Get(href + "?dismissed=1").Body.String()
	has(t, "dismissed", body, "Dismissed. The next refresh checks again", ">ok<")
	if strings.Contains(body, "was held back") {
		t.Error("the band outlived Dismiss")
	}
	if rec = h.Login.Post(snap+"/accept", url.Values{}); rec.Header().Get("Location") != href+"?gone=1" {
		t.Errorf("accepting a dismissed snapshot: %s", rec.Header().Get("Location"))
	}

	// held back again; Accept anyway
	h.Up.V2fly("netflix", nfNames(131, 215))
	if _, err := h.Mod.Refresh.Refresh(bg(), id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	w, _, _ = h.Mod.Refresh.Waiting(bg(), id)
	rec = h.Login.Post(fmt.Sprintf("%s/snapshots/%d/accept", href, w.ID), url.Values{})
	if rec.Header().Get("Location") != href+"?accepted=1" {
		t.Fatalf("accept: %s", rec.Header().Get("Location"))
	}
	body = h.Login.Get(href + "?accepted=1").Body.String()
	has(t, "accepted", body, "netflix now has 84 domains: accepted anyway.")
	if acc, _ := h.Mod.Services.Accepted(bg(), id); acc.Count() != 84 || acc.ForcedBy != "admin" {
		t.Errorf("accepted: %d %q", acc.Count(), acc.ForcedBy)
	}

	// a failing refresh in the Source area
	h.Up.Fail("/v2fly/data/netflix", http.StatusNotFound)
	_, _ = h.Mod.Refresh.Refresh(bg(), id, false, "admin")
	body = h.Login.Get(href).Body.String()
	has(t, "failing", body, "Failing · 1 failure in a row · v2fly has no list", ">failing<")

	// a custom service has no Refresh now
	c := h.Custom("mine", "example.com")
	if body := h.Login.Get(fmt.Sprintf("/routing/services/%d", c)).Body.String(); strings.Contains(body, ">Refresh now<") {
		t.Error("a custom service offers Refresh now")
	}
	if rec := h.Login.Post(fmt.Sprintf("/routing/services/%d/refresh", c), url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("refreshing a custom service: %d", rec.Code)
	}
}

func bg() context.Context { return context.Background() }

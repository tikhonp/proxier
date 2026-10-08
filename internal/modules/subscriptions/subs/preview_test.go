package subs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func hideOn(t *testing.T, h *substest.Harness, id int64) {
	t.Helper()
	st := settingsOf(get(t, h, id))
	st.HideOn = true
	if _, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewHidesUnhealthy(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1, 2)
	hideOn(t, h, id)
	h.Catalog.SetHealth(2, "blocked", h.Now.Add(-10*time.Minute))
	resp, _, err := h.Mod.Subs.Preview(en(h), id, "", true)
	if err != nil || len(resp.Lines) != 2 || len(resp.Hidden) != 0 {
		t.Fatalf("10 min: %v %v", resp.Lines, err)
	}
	h.Advance(21 * time.Minute)
	resp, _, err = h.Mod.Subs.Preview(en(h), id, "", true)
	if err != nil || len(resp.Lines) != 1 || !strings.Contains(resp.Lines[0], "nl-1") || len(resp.Hidden) != 1 || resp.Hidden[0].For != 31*time.Minute {
		t.Fatalf("31 min: %v %+v %v", resp.Lines, resp.Hidden, err)
	}
	// the page's words for it
	body := h.Login.Get("/subscriptions/1").Body.String()
	if !strings.Contains(body, "de-1 hidden: blocked for 31 min (more than 30 min)") {
		t.Error("the preview does not say why de-1 is hidden")
	}
	h.Catalog.SetHealth(2, "healthy", h.Now)
	if resp, _, _ := h.Mod.Subs.Preview(en(h), id, "", true); len(resp.Lines) != 2 {
		t.Error("de-1 did not come back")
	}
}

func TestPreviewAllUnhealthy(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1, 2)
	hideOn(t, h, id)
	h.Catalog.SetHealth(1, "down", h.Now.Add(-time.Hour))
	h.Catalog.SetHealth(2, "down", h.Now.Add(-time.Hour))
	before := len(h.Events(""))
	resp, _, err := h.Mod.Subs.Preview(en(h), id, "", true)
	if err != nil || len(resp.Lines) != 2 || !resp.AllHidden {
		t.Fatalf("%v %v %v", resp.Lines, resp.AllHidden, err)
	}
	if body := h.Login.Get("/subscriptions/1").Body.String(); !strings.Contains(body, "Every server would be hidden, so all are served") {
		t.Error("the page does not say all are served")
	}
	if after := len(h.Events("")); after != before {
		t.Errorf("a preview recorded %d events", after-before)
	}
}

func TestMemberOutOfService(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1, 2)
	h.Catalog.Drop(2)
	resp, members, err := h.Mod.Subs.Preview(en(h), id, "", true)
	if err != nil || len(resp.Lines) != 1 || strings.Contains(resp.Lines[0], "de-1") {
		t.Fatalf("%v %v", resp.Lines, err)
	}
	if len(members) != 2 || members[1].InService || members[1].Name != "de-1" || members[1].Flag != "" {
		t.Errorf("members: %+v", members)
	}
	body := h.Login.Get("/subscriptions/1").Body.String()
	if !strings.Contains(body, "not in service") || !strings.Contains(body, "de-1 is not in service, so it isn&#39;t served") {
		t.Error("the page does not list de-1 as not in service")
	}
	h.Catalog.Drop(1)
	resp, _, _ = h.Mod.Subs.Preview(en(h), id, "", true)
	if resp.Outcome != output.StubEmpty || resp.Lines[0] != output.StubURI("⚠️ No servers yet") {
		t.Errorf("all dropped: %s %v", resp.Outcome, resp.Lines)
	}
}

package refresh_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestDigestNotifiesOnlyWithNews(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("a", "a.com\n")
	h.Up.V2fly("b", "b.com\n")
	a, b := h.Upstream("v2fly:a"), h.Upstream("v2fly:b")
	h.List("Main", a, b)

	// nothing new: recorded, silent
	d := round(t, h)
	if notifies(t, h, d) || num(d.Payload["services"]) != 2 {
		t.Errorf("quiet round: %+v", d.Payload)
	}
	// a starts failing: news
	h.Advance(24 * time.Hour)
	h.Up.Fail("/v2fly/data/a", http.StatusNotFound)
	d = round(t, h)
	if !notifies(t, h, d) || num(d.Payload["failing"]) != 1 || num(d.Payload["still_failing"]) != 0 {
		t.Errorf("first failing day: %+v", d.Payload)
	}
	// a fails for a second day: counted, not news
	h.Advance(24 * time.Hour)
	d = round(t, h)
	if notifies(t, h, d) || num(d.Payload["failing"]) != 0 || num(d.Payload["still_failing"]) != 1 {
		t.Errorf("second failing day: %+v", d.Payload)
	}
	// b changes: news, with a still failing in the counts
	h.Advance(24 * time.Hour)
	h.Up.V2fly("b", "b.com\nb.net\n")
	d = round(t, h)
	if !notifies(t, h, d) || num(d.Payload["changed"]) != 1 || num(d.Payload["still_failing"]) != 1 {
		t.Errorf("a change: %+v", d.Payload)
	}
	// b held back: news
	h.Advance(24 * time.Hour)
	h.Up.V2fly("b", "")
	d = round(t, h)
	if !notifies(t, h, d) || num(d.Payload["rejected"]) != 1 || num(d.Payload["changed"]) != 0 {
		t.Errorf("a rejection: %+v", d.Payload)
	}
	if last, ok, err := h.Mod.Refresh.LastDigest(bg); err != nil || !ok || last.ID != d.ID {
		t.Errorf("LastDigest: %+v %v %v", last, ok, err)
	}
}

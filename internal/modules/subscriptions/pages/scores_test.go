package pages_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/access"
)

func TestScoreNameSettingsPersistValidateAndRecordChanges(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1)
	form := formValues(h, id)
	form.Set("score_names", "chatgpt")
	if rec := h.Login.Post(subPath(id), form); rec.Code != http.StatusSeeOther {
		t.Fatalf("save %d %s", rec.Code, rec.Body)
	}
	s, err := h.Mod.Subs.Get(t.Context(), id, access.All)
	if err != nil || s.ScoreNames != "chatgpt" {
		t.Fatalf("setting not saved: %+v %v", s, err)
	}
	if ev := h.Events("subscription.updated"); len(ev) != 1 || !strings.Contains(ev[0].Payload["changes"].(string), "score in endpoint names") {
		t.Fatalf("events: %v", ev)
	}
	if rec := h.Login.Post(subPath(id), form); rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	if len(h.Events("subscription.updated")) != 1 {
		t.Fatal("unchanged save recorded event")
	}
	body := h.Login.Get(subPath(id)).Body.String()
	if !strings.Contains(body, `value="chatgpt" selected`) {
		t.Fatal("saved choice not selected")
	}
	form.Set("score_names", "bogus")
	rec := h.Login.Post(subPath(id), form)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Choose Off, IP reputation, Claude or ChatGPT.") {
		t.Fatalf("bad mode: %d", rec.Code)
	}
	s, _ = h.Mod.Subs.Get(t.Context(), id, access.All)
	if s.ScoreNames != "chatgpt" || len(h.Events("subscription.updated")) != 1 {
		t.Fatal("invalid setting changed subscription")
	}
}

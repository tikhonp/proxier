package links_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

const day = 24 * time.Hour

// expiring creates a link of a new subscription holding nl-1 that expires
// after d, and returns its id and token.
func expiring(t *testing.T, h *substest.Harness, d time.Duration) (int64, string) {
	t.Helper()
	sub := h.Subscription("Family", 1)
	id, err := h.Mod.Links.Create(bg, links.New{Name: "Mom", SubscriptionID: sub, Expires: h.Now.Add(d), Lang: i18n.EN}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.Mod.Links.Token(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

// scan runs the expiry step at the harness's now.
func scan(t *testing.T, h *substest.Harness) {
	t.Helper()
	if err := h.Mod.Links.ExpiryStep(bg, h.Now, "job:1"); err != nil {
		t.Fatal(err)
	}
}

func count(h *substest.Harness, typ string) int { return len(h.Events(typ)) }

func TestExpiringSoonOnce(t *testing.T) {
	h := substest.New(t)
	id, _ := expiring(t, h, 2*day)
	scan(t, h)
	evs := h.Events("link.expiring_soon")
	if len(evs) != 1 {
		t.Fatalf("expiring_soon: %d", len(evs))
	}
	e := evs[0]
	if e.Subject != links.Subject(id) || e.Actor != "job:1" || e.Payload["expiry"] != h.Now.Add(2*day).UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Errorf("event: %+v", e)
	}
	for range 8 {
		h.Advance(15 * time.Minute)
		scan(t, h)
	}
	h.Advance(2*day - 3*time.Hour)
	scan(t, h)
	if n := count(h, "link.expiring_soon"); n != 1 {
		t.Errorf("repeated: %d", n)
	}
	if n := count(h, "link.expired"); n != 0 {
		t.Errorf("expired early: %d", n)
	}
}

func TestExpiredOnce(t *testing.T) {
	h := substest.New(t)
	id, token := expiring(t, h, time.Hour)
	h.Advance(time.Hour + time.Second)
	// the fetch serves the stub from the clock alone, before any scan
	if body := h.Fetch("GET", "/s/"+token, "", "").Body.String(); !strings.Contains(body, "Expired") {
		t.Errorf("fetch before the scan: %q", body)
	}
	scan(t, h)
	evs := h.Events("link.expired")
	if len(evs) != 1 || evs[0].Subject != links.Subject(id) || evs[0].Payload["expiry"] == "" {
		t.Fatalf("expired: %+v", evs)
	}
	// it expired before it was ever warned: only link.expired
	if n := count(h, "link.expiring_soon"); n != 0 {
		t.Errorf("warned after expiring: %d", n)
	}
	for range 4 {
		h.Advance(15 * time.Minute)
		scan(t, h)
	}
	h.Advance(10 * day)
	scan(t, h)
	if n := count(h, "link.expired"); n != 1 {
		t.Errorf("repeated: %d", n)
	}
}

func TestExtendingWarnsAgain(t *testing.T) {
	h := substest.New(t)
	id, _ := expiring(t, h, 2*day)
	scan(t, h)
	h.Advance(time.Hour)
	if err := h.Mod.Links.SetExpiry(bg, id, h.Now.Add(60*time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	scan(t, h)
	if n := count(h, "link.expiring_soon"); n != 2 {
		t.Errorf("a new expiry within 3 days: %d warnings", n)
	}
	// extended far away: nothing until it comes within 3 days again
	if err := h.Mod.Links.SetExpiry(bg, id, h.Now.Add(10*day), "admin"); err != nil {
		t.Fatal(err)
	}
	scan(t, h)
	if n := count(h, "link.expiring_soon"); n != 2 {
		t.Errorf("far away: %d", n)
	}
	h.Advance(7*day + time.Hour)
	scan(t, h)
	if n := count(h, "link.expiring_soon"); n != 3 {
		t.Errorf("within 3 days again: %d", n)
	}
}

func TestDisabledLinkExpiresQuietly(t *testing.T) {
	h := substest.New(t)
	id, _ := expiring(t, h, time.Hour)
	if err := h.Mod.Links.Disable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	scan(t, h)
	h.Advance(2 * time.Hour)
	scan(t, h)
	if n := count(h, "link.expired") + count(h, "link.expiring_soon"); n != 0 {
		t.Fatalf("a disabled link notified: %d", n)
	}
	if err := h.Mod.Links.Enable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	scan(t, h)
	if n := count(h, "link.expired"); n != 1 {
		t.Errorf("enabled: %d link.expired", n)
	}
}

func TestTombstoneTokenErased(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, token := h.Link(sub, "Alex")
	if err := h.Mod.Links.Delete(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	// inside the tombstone the token stays
	h.Advance(29 * day)
	if err := h.Mod.Links.TombstoneStep(bg, h.Now); err != nil {
		t.Fatal(err)
	}
	if n := tokens(t, h, id); n != 1 {
		t.Fatalf("erased inside the tombstone")
	}
	h.Advance(2 * day)
	before := allEvents(t, h)
	if err := h.Mod.Links.TombstoneStep(bg, h.Now); err != nil {
		t.Fatal(err)
	}
	if n := tokens(t, h, id); n != 0 {
		t.Fatalf("token kept after 31 days")
	}
	if after := allEvents(t, h); after != before {
		t.Errorf("erasing recorded %d events", after-before)
	}
	if rec := h.Fetch("GET", "/s/"+token, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("fetch: %d", rec.Code)
	}
	if rec := h.Login.Get("/links/" + strconv.FormatInt(id, 10)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ">Alex<") {
		t.Errorf("page: %d", rec.Code)
	}
}

// tokens counts the link's stored tokens (1 or 0) and their lookups.
func tokens(t *testing.T, h *substest.Harness, id int64) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.GetContext(bg, &n, `SELECT count(*) FROM subs_links WHERE id = ? AND token IS NOT NULL AND token_lookup IS NOT NULL`, id); err != nil {
		t.Fatal(err)
	}
	return n
}

func allEvents(t *testing.T, h *substest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.GetContext(bg, &n, `SELECT count(*) FROM events`); err != nil {
		t.Fatal(err)
	}
	return n
}

func fetches(t *testing.T, h *substest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.GetContext(bg, &n, `SELECT count(*) FROM subs_fetches`); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFetchRetention(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, _ := h.Link(sub, "Alex")
	for _, ago := range []time.Duration{100 * day, 60 * day, 20 * day, day} {
		h.FetchAt(id, h.Now.Add(-ago), "198.51.100.7", "Happ/1.0")
	}
	if err := h.Mod.Links.PruneStep(bg, h.Now); err != nil {
		t.Fatal(err)
	}
	if n := fetches(t, h); n != 3 {
		t.Errorf("default 90 days: %d left", n)
	}
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.fetch_retention": "720h"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.PruneStep(bg, h.Now); err != nil {
		t.Fatal(err)
	}
	if n := fetches(t, h); n != 2 {
		t.Errorf("30 days: %d left", n)
	}
}

func TestExpiryScanJob(t *testing.T) {
	h := substest.New(t)
	var sch *jobs.Schedule
	for _, s := range h.Mod.Schedules() {
		if s.Name == links.JobExpiryScan {
			sch = &s
		}
	}
	if sch == nil || sch.Every != 15*time.Minute {
		t.Fatalf("schedule: %+v", sch)
	}
	if req, err := sch.Request(bg); err != nil || req.Type != links.JobExpiryScan {
		t.Fatalf("schedule request: %+v %v", req, err)
	}
	expiring(t, h, 2*day)
	h.StartJobs()
	e, err := h.App.Jobs.EnqueueNow(bg, jobs.Request{Type: links.JobExpiryScan, CreatedBy: "schedule:" + links.JobExpiryScan})
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	j, err := h.App.Jobs.Job(bg, e.ID)
	if err != nil || j.State != jobs.Succeeded {
		t.Fatalf("job: %+v %v", j, err)
	}
	steps, err := h.App.Jobs.Steps(bg, e.ID)
	if err != nil || len(steps) != 3 {
		t.Fatalf("steps: %+v %v", steps, err)
	}
	for i, name := range []string{"expiry", "tombstones", "prune"} {
		if steps[i].Name != name || steps[i].State != "succeeded" {
			t.Errorf("step %d: %+v", i, steps[i])
		}
	}
	evs := h.Events("link.expiring_soon")
	if len(evs) != 1 || evs[0].Actor != events.JobActor(e.ID) {
		t.Errorf("the job's warning: %+v", evs)
	}
}

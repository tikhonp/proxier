package fetch_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var bg = context.Background()

// family is the harness with "Family" = nl-1, de-1 and a link "Mom" to it.
func family(t *testing.T) (*substest.Harness, int64, int64, string) {
	t.Helper()
	h := substest.New(t)
	sub := h.Subscription("Family", 1, 2)
	id, token := h.Link(sub, "Mom")
	return h, sub, id, token
}

// raw reads a header by its exact (lower-case) name: the fetch sends them
// as written, not canonicalised.
func raw(h http.Header, name string) []string { return h[name] }

func lines(body string) []string { return strings.Split(strings.TrimSuffix(body, "\n"), "\n") }

func fetches(t *testing.T, h *substest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.GetContext(bg, &n, `SELECT count(*) FROM subs_fetches`); err != nil {
		t.Fatal(err)
	}
	return n
}

// stub is the one entry a stubbed answer holds.
func stub(t *testing.T, body string) string {
	t.Helper()
	ls := lines(body)
	if len(ls) != 1 {
		t.Fatalf("not one stub entry: %q", body)
	}
	return ls[0]
}

func mustStub(t *testing.T, h *substest.Harness, token, text string) {
	t.Helper()
	rec := h.Fetch("GET", "/s/"+token, "", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := stub(t, rec.Body.String()); got != output.StubURI(text) {
		t.Fatalf("stub %q, want %q", got, output.StubURI(text))
	}
}

func mustServe(t *testing.T, h *substest.Harness, token string, names ...string) []string {
	t.Helper()
	rec := h.Fetch("GET", "/s/"+token, "", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	ls := lines(rec.Body.String())
	if len(ls) != len(names) {
		t.Fatalf("%d lines, want %v: %q", len(ls), names, rec.Body.String())
	}
	for i, n := range names {
		if !strings.HasPrefix(ls[i], "vless://") || !strings.HasSuffix(ls[i], "#"+output.Escape(n)) {
			t.Errorf("line %d is %q, want %s", i, ls[i], n)
		}
	}
	return ls
}

func settings(t *testing.T, h *substest.Harness, id int64, change func(*subs.Settings)) {
	t.Helper()
	s, err := h.Mod.Subs.Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	st := subs.Settings{Name: s.Name, Title: s.Title, Description: s.Description, Formats: s.Formats, DefaultFormat: s.DefaultFormat,
		UpdateHours: s.UpdateHours, HideOn: s.Hide.On, HideStates: s.Hide.States, GraceMinutes: int(s.Hide.Grace / time.Minute), AutoAdd: s.AutoAdd}
	change(&st)
	if _, err := h.Mod.Subs.Update(bg, id, st, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchServesURIsAndHeaders(t *testing.T) {
	h, _, _, token := family(t)
	rec := h.Fetch("GET", "/s/"+token, "", "Happ/3.2.1")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	ls := mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	if !strings.Contains(ls[0], substest.Credential(1)+"@nl-1.hosts.tikhonnnnn.com:443") {
		t.Errorf("nl-1's URI: %s", ls[0])
	}
	hd := rec.Header()
	for k, want := range map[string]string{
		"Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store", "X-Robots-Tag": "noindex, nofollow",
	} {
		if hd.Get(k) != want {
			t.Errorf("%s: %q", k, hd.Get(k))
		}
	}
	for k, want := range map[string]string{
		"profile-title":           "base64:" + base64.StdEncoding.EncodeToString([]byte("Family")),
		"profile-update-interval": "12",
		"subscription-userinfo":   "upload=0; download=0; total=0; expire=0",
		"content-disposition":     "inline; filename*=UTF-8''Family.txt",
	} {
		if got := hd[k]; len(got) != 1 || got[0] != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if fetches(t, h) != 2 {
		t.Errorf("%d fetches recorded", fetches(t, h))
	}
}

func TestFetchBase64Format(t *testing.T) {
	h, _, _, token := family(t)
	plain := h.Fetch("GET", "/s/"+token, "", "").Body.String()
	rec := h.Fetch("GET", "/s/"+token+"?format=uri-base64", "", "")
	if rec.Code != 200 || rec.Body.String() != base64.StdEncoding.EncodeToString([]byte(plain)) {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	var format string
	_ = h.App.DB.R.GetContext(bg, &format, `SELECT format FROM subs_fetches ORDER BY id DESC LIMIT 1`)
	if format != "uri-base64" {
		t.Errorf("recorded format %q", format)
	}
}

func TestFetchBadFormat(t *testing.T) {
	h, sub, _, token := family(t)
	if rec := h.Fetch("GET", "/s/"+token+"?format=mihomo", "", ""); rec.Code != 400 {
		t.Errorf("mihomo: %d", rec.Code)
	}
	settings(t, h, sub, func(s *subs.Settings) { s.Formats = []string{"uri-plain"} })
	if rec := h.Fetch("GET", "/s/"+token+"?format=uri-base64", "", ""); rec.Code != 400 {
		t.Errorf("not allowed: %d", rec.Code)
	}
	if fetches(t, h) != 0 {
		t.Errorf("%d fetches recorded", fetches(t, h))
	}
}

func TestUnknownTokenIs404(t *testing.T) {
	h, _, _, _ := family(t)
	want := h.Fetch("GET", "/s/nope/extra", "", "")
	if want.Code != 404 {
		t.Fatalf("/s/nope/extra: %d", want.Code)
	}
	for _, p := range []string{"/s/" + strings.Repeat("A", 43), "/s/not%20a%20token!", "/s/short", "/s/", "/s"} {
		rec := h.Fetch("GET", p, "", "")
		if rec.Code != 404 || rec.Body.String() != want.Body.String() || rec.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Errorf("%s: %d %q (want %q)", p, rec.Code, rec.Body.String(), want.Body.String())
		}
		if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("Location") != "" {
			t.Errorf("%s: a cookie or a redirect", p)
		}
	}
	if fetches(t, h) != 0 {
		t.Errorf("%d fetches recorded", fetches(t, h))
	}
}

func TestDisableAndEnable(t *testing.T) {
	h, _, id, token := family(t)
	if err := h.Mod.Links.Disable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	mustStub(t, h, token, "⛔ Link disabled · contact @tikhonp")
	if err := h.App.Settings.Set(bg, "admin", "general", map[string]string{"general.admin_contact": ""}); err != nil {
		t.Fatal(err)
	}
	mustStub(t, h, token, "⛔ Link disabled")
	if err := h.Mod.Links.Enable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	var outcomes []string
	_ = h.App.DB.R.SelectContext(bg, &outcomes, `SELECT outcome FROM subs_fetches ORDER BY id`)
	if strings.Join(outcomes, ",") != "stub-disabled,stub-disabled,ok" {
		t.Errorf("outcomes %v", outcomes)
	}
}

// expire sets an expiry from the form's fields.
func expire(t *testing.T, h *substest.Harness, id int64, date, clock string) {
	t.Helper()
	at, err := linksParse(h, date, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.SetExpiry(bg, id, at, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredStubInRussian(t *testing.T) {
	h, _, id, token := family(t)
	if err := h.Mod.Links.Edit(bg, id, editOf(h, id, func(e *editForm) { e.Lang = "ru" }), "admin"); err != nil {
		t.Fatal(err)
	}
	expire(t, h, id, "2026-12-01", "")
	h.Now = time.Date(2026, 12, 3, 9, 0, 0, 0, time.UTC)
	mustStub(t, h, token, "⏳ Срок истёк 1 дек 2026 · пишите @tikhonp")
}

func TestDateOnlyExpiryEndsAtMidnight(t *testing.T) {
	h, _, id, token := family(t)
	expire(t, h, id, "2026-12-01", "")
	msk, _ := time.LoadLocation("Europe/Moscow")
	h.Now = time.Date(2026, 12, 1, 23, 59, 59, 0, msk)
	rec := h.Fetch("GET", "/s/"+token, "", "")
	if got := raw(rec.Header(), "subscription-userinfo"); len(got) != 1 || got[0] != "upload=0; download=0; total=0; expire=1796158799" {
		t.Errorf("userinfo %q", got)
	}
	mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	h.Now = time.Date(2026, 12, 2, 0, 0, 0, 0, msk)
	mustStub(t, h, token, "⏳ Expired on 1 Dec 2026 · contact @tikhonp")
}

func TestExtendExpiredLink(t *testing.T) {
	h, _, id, token := family(t)
	expire(t, h, id, "2026-10-10", "")
	h.Now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	mustStub(t, h, token, "⏳ Expired on 10 Oct 2026 · contact @tikhonp")
	expire(t, h, id, "2026-11-30", "")
	mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
}

func TestDisabledWinsOverExpired(t *testing.T) {
	h, _, id, token := family(t)
	expire(t, h, id, "2026-10-10", "")
	if err := h.Mod.Links.Disable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	mustStub(t, h, token, "⛔ Link disabled · contact @tikhonp")
}

func TestDeletedLinkTombstone(t *testing.T) {
	h, _, id, token := family(t)
	if err := h.Mod.Links.Delete(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Advance(10 * 24 * time.Hour)
	mustStub(t, h, token, "⛔ Link removed")
	h.Advance(21 * 24 * time.Hour)
	if rec := h.Fetch("GET", "/s/"+token, "", ""); rec.Code != 404 {
		t.Errorf("31 days later: %d", rec.Code)
	}
	if body := h.Login.Get("/links").Body.String(); strings.Contains(body, ">Mom<") {
		t.Error("a deleted link is in the default list")
	}
}

func TestFetchEmptySubscriptionStub(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Empty")
	_, token := h.Link(sub, "Mom")
	mustStub(t, h, token, "⚠️ No servers yet")
	var outcome string
	_ = h.App.DB.R.GetContext(bg, &outcome, `SELECT outcome FROM subs_fetches`)
	if outcome != output.StubEmpty {
		t.Errorf("outcome %q", outcome)
	}
}

func hideBlockedAndDown(s *subs.Settings) {
	s.HideOn, s.HideStates, s.GraceMinutes = true, []string{"blocked", "down"}, 30
}

func TestFetchHidesUnhealthy(t *testing.T) {
	h, sub, _, token := family(t)
	settings(t, h, sub, hideBlockedAndDown)
	h.Catalog.SetHealth(2, "blocked", h.Now.Add(-10*time.Minute))
	mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	h.Advance(21 * time.Minute)
	mustServe(t, h, token, "🇳🇱 Netherlands 1")
	if n := len(h.Events("subscription.all_unhealthy")); n != 0 {
		t.Errorf("%d all_unhealthy", n)
	}
}

func TestAllUnhealthyOncePerHour(t *testing.T) {
	h, sub, _, token := family(t)
	settings(t, h, sub, hideBlockedAndDown)
	h.Catalog.SetHealth(1, "down", h.Now.Add(-2*time.Hour))
	h.Catalog.SetHealth(2, "blocked", h.Now.Add(-2*time.Hour))
	mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	ev := h.Events("subscription.all_unhealthy")
	if len(ev) != 1 || ev[0].Payload["servers"] != "nl-1, de-1" || ev[0].Payload["count"] != float64(2) || ev[0].Actor != "system" ||
		ev[0].Subject.String() != "subscription:1" {
		t.Fatalf("events: %+v", ev)
	}
	h.Advance(30 * time.Minute)
	mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	if n := len(h.Events("subscription.all_unhealthy")); n != 1 {
		t.Errorf("30 min later: %d events", n)
	}
	h.Advance(31 * time.Minute)
	mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	if n := len(h.Events("subscription.all_unhealthy")); n != 2 {
		t.Errorf("61 min later: %d events", n)
	}
}

func TestFetchAfterRotation(t *testing.T) {
	h, _, _, token := family(t)
	old := mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	const fresh = "0b7d2e61-5f4c-4a8e-9d3b-2c1e0f9a8b7c"
	h.Catalog.SetCredential(1, fresh)
	h.Advance(time.Second)
	now := mustServe(t, h, token, "🇳🇱 Netherlands 1", "🇩🇪 Germany 1")
	if !strings.HasPrefix(now[0], "vless://"+fresh+"@") || strings.Contains(now[0], substest.Credential(1)) {
		t.Errorf("nl-1 after the rotation: %s", now[0])
	}
	if now[1] != old[1] {
		t.Error("de-1 changed")
	}
}

func TestFetchFollowsServerOrder(t *testing.T) {
	h, sub, _, token := family(t)
	if err := h.Mod.Subs.Move(bg, sub, 2, false, "admin"); err != nil {
		t.Fatal(err)
	}
	mustServe(t, h, token, "🇩🇪 Germany 1", "🇳🇱 Netherlands 1")
}

func TestFetchRateLimited(t *testing.T) {
	h, _, _, token := family(t)
	for i := 0; i < 60; i++ {
		if rec := h.Fetch("GET", "/s/"+token, "198.51.100.23:5000", ""); rec.Code != 200 {
			t.Fatalf("request %d: %d", i+1, rec.Code)
		}
	}
	h.Advance(20 * time.Second)
	rec := h.Fetch("GET", "/s/"+token, "198.51.100.23:5000", "")
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "40" {
		t.Fatalf("61st: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := h.Fetch("GET", "/s/"+token, "203.0.113.50:5000", ""); rec.Code != 200 {
		t.Errorf("another IP: %d", rec.Code)
	}
	h.Advance(41 * time.Second)
	if rec := h.Fetch("GET", "/s/"+token, "198.51.100.23:5000", ""); rec.Code != 200 {
		t.Errorf("the next window: %d", rec.Code)
	}
}

func TestFetchRecordsClientIP(t *testing.T) {
	h, _, _, token := family(t)
	hd := http.Header{"X-Real-Ip": {"203.0.113.9"}}
	if rec := h.Site.Do(sitetest.Req{Path: "/s/" + token, Addr: substest.Tunnel, Header: hd}); rec.Code != 200 {
		t.Fatalf("through the tunnel: %d", rec.Code)
	}
	if rec := h.Site.Do(sitetest.Req{Path: "/s/" + token, Addr: "198.51.100.7:6000", Header: hd}); rec.Code != 200 {
		t.Fatalf("direct: %d", rec.Code)
	}
	var rows []struct {
		IP      string `db:"ip"`
		Network string `db:"network"`
	}
	if err := h.App.DB.R.SelectContext(bg, &rows, `SELECT ip, network FROM subs_fetches ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].IP != "203.0.113.9" || rows[0].Network != "203.0.113.0/24" ||
		rows[1].IP != "198.51.100.7" || rows[1].Network != "198.51.100.0/24" {
		t.Errorf("recorded %+v", rows)
	}
}

func TestHeadRecordsNothing(t *testing.T) {
	h, _, _, token := family(t)
	get := h.Fetch("GET", "/s/"+token, "", "")
	head := h.Fetch("HEAD", "/s/"+token, "", "")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d, %d bytes", head.Code, head.Body.Len())
	}
	for k, v := range get.Header() {
		if k == "Content-Length" || k == "Date" || k == "X-Request-Id" {
			continue
		}
		if strings.Join(head.Header()[k], "|") != strings.Join(v, "|") {
			t.Errorf("%s: HEAD %q, GET %q", k, head.Header()[k], v)
		}
	}
	if fetches(t, h) != 1 {
		t.Errorf("%d fetches recorded (only the GET counts)", fetches(t, h))
	}
}

func TestTitleHeaderIsBase64(t *testing.T) {
	h, sub, _, token := family(t)
	settings(t, h, sub, func(s *subs.Settings) { s.Title = "Семья" })
	rec := h.Fetch("GET", "/s/"+token, "", "")
	if got := raw(rec.Header(), "profile-title"); len(got) != 1 || got[0] != "base64:0KHQtdC80YzRjw==" {
		t.Errorf("profile-title %q", got)
	}
}

func TestLongUserAgentIsTrimmed(t *testing.T) {
	h, _, _, token := family(t)
	ua := "Happ/1.0 " + strings.Repeat("ж", 1020) // more than 2 KB
	if rec := h.Fetch("GET", "/s/"+token, "", ua); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var row struct {
		UA  string `db:"user_agent"`
		App string `db:"app"`
	}
	if err := h.App.DB.R.GetContext(bg, &row, `SELECT user_agent, app FROM subs_fetches`); err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(row.UA) != 256 || !strings.HasPrefix(ua, row.UA) || row.App != "Happ" {
		t.Errorf("stored %d characters, app %q", utf8.RuneCountInString(row.UA), row.App)
	}
}

func swapCase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case 'a' <= c && c <= 'z':
			b[i] = c - 32
		case 'A' <= c && c <= 'Z':
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestTokenPathRules(t *testing.T) {
	h, _, _, token := family(t)
	plain := h.Fetch("GET", "/s/"+token, "", "")
	slash := h.Fetch("GET", "/s/"+token+"/", "", "")
	if slash.Code != 200 || slash.Body.String() != plain.Body.String() {
		t.Errorf("trailing slash: %d", slash.Code)
	}
	if rec := h.Fetch("GET", "/s/"+swapCase(token), "", ""); rec.Code != 404 {
		t.Errorf("other letter case: %d", rec.Code)
	}
}

func TestFetchTouchesNoCookies(t *testing.T) {
	h, _, _, token := family(t)
	plain := h.Fetch("GET", "/s/"+token, "", "")
	rec := h.Site.Do(sitetest.Req{Path: "/s/" + token, Addr: "198.51.100.23:5000", Cookies: []*http.Cookie{h.Login.Cookie, {Name: "x", Value: "y"}}})
	if rec.Code != 200 || rec.Body.String() != plain.Body.String() {
		t.Errorf("with cookies: %d", rec.Code)
	}
	for _, r := range []*http.Response{plain.Result(), rec.Result()} {
		if len(r.Cookies()) != 0 || r.Header.Get("Set-Cookie") != "" {
			t.Errorf("a cookie was set: %v", r.Cookies())
		}
	}
}

func TestFetchUpdatesLastFetch(t *testing.T) {
	h, _, id, token := family(t)
	h.Advance(time.Minute)
	if rec := h.Fetch("GET", "/s/"+token, "[2001:db8:4f2:1::7]:5000", "v2RayTun/2.1 (iOS)"); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	l, err := h.Mod.Links.Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if !l.LastFetchAt.Equal(h.Now) || l.LastFetchApp != "v2RayTun" || l.LastFetchNetwork != "2001:db8:4f2::/48" {
		t.Errorf("last fetch %v %q %q", l.LastFetchAt, l.LastFetchApp, l.LastFetchNetwork)
	}
}

type editForm = links.Edit

// editOf is the link's Edit form after change.
func editOf(h *substest.Harness, id int64, change func(*links.Edit)) links.Edit {
	h.T.Helper()
	l, err := h.Mod.Links.Get(bg, id)
	if err != nil {
		h.T.Fatal(err)
	}
	e := links.Edit{Name: l.Name, Note: l.Note, Lang: l.Lang, Format: l.Format}
	change(&e)
	return e
}

func linksParse(h *substest.Harness, date, clock string) (time.Time, error) {
	return links.ParseExpiry("on", date, clock, h.Mod.Links.Zone(bg), h.Now)
}

package pages_test

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func TestLinkList(t *testing.T) {
	h := substest.New(t)
	family := h.Subscription("Family", 1)
	friends := h.Subscription("Friends", 2)
	_, momToken := h.Link(family, "Mom")
	dad, dadToken := h.Link(family, "Dad")
	alex, _ := h.Link(friends, "Alex")
	gone, _ := h.Link(friends, "Old phone")
	soon, _ := h.Link(friends, "Sam")
	if err := h.Mod.Links.Disable(h.T.Context(), dad, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.SetExpiry(h.T.Context(), soon, h.Now.Add(3*24*time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.SetExpiry(h.T.Context(), alex, h.Now.Add(time.Hour), "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.Delete(h.T.Context(), gone, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Advance(2 * time.Hour) // Alex has expired
	if rec := h.Fetch("GET", "/s/"+momToken, "", "Streisand/2.3"); rec.Code != 200 {
		t.Fatal(rec.Code)
	}

	body := page(t, h, "/links")
	mustContain(t, body, "Links", "2 active · 1 expiring this week", "New link", `data-key="n"`,
		"Mom", "Dad", "Alex", "Sam", "just now · Streisand", "never fetched", "expired 7 Oct", "10 Oct, 15:00 · in 2 days",
		"1 deleted link still serves “Link removed”", `href="/links?state=deleted"`)
	mustNotContain(t, body, "Old phone", momToken, dadToken)
	noInline(t, body)
	// sorted by last fetch, then by name
	if strings.Index(body, ">Mom<") > strings.Index(body, ">Alex<") || strings.Index(body, ">Alex<") > strings.Index(body, ">Dad<") {
		t.Error("not sorted by last fetch, then name")
	}

	rows := func(path string) []string {
		var out []string
		for _, name := range []string{"Mom", "Dad", "Alex", "Sam", "Old phone"} {
			if strings.Contains(page(t, h, path), ">"+name+"<") {
				out = append(out, name)
			}
		}
		return out
	}
	for path, want := range map[string]string{
		"/links?state=deleted":  "Old phone",
		"/links?state=disabled": "Dad",
		"/links?state=expired":  "Alex",
		"/links?state=active":   "Mom,Sam",
		"/links?state=all":      "Mom,Dad,Alex,Sam,Old phone",
		"/links?subscription=" + strconv.FormatInt(friends, 10): "Alex,Sam",
		"/links?expiring=1": "Sam",
		"/links?name=A&subscription=" + strconv.FormatInt(friends, 10):              "Alex,Sam",
		"/links?name=a&state=active&subscription=" + strconv.FormatInt(friends, 10): "Sam",
		"/links?name=zzz": "",
	} {
		if got := strings.Join(rows(path), ","); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
	// the filters stay in the URL: the chip keeps the others, Clear drops them
	body = page(t, h, "/links?name=a&state=active")
	mustContain(t, body, `value="a"`, `<option value="active" selected`, `href="/links?expiring=1&amp;name=a&amp;state=active"`, `href="/links" class="pc-chip" data-key="x"`)
	if !strings.Contains(page(t, h, "/links?state=deleted"), "Old phone") || strings.Contains(page(t, h, "/links?state=deleted"), "still serve") {
		t.Error("the deleted filter shows the footer")
	}
}

func TestNewLinkForm(t *testing.T) {
	h := substest.New(t)
	body := page(t, h, "/links/new")
	mustContain(t, body, "A link serves a subscription, and there is none yet.", `href="/subscriptions/new"`)

	family := h.Subscription("Family", 1, 2)
	body = page(t, h, "/links/new")
	mustContain(t, body, `<option value="1" selected>Family · 2 servers</option>`, `<option value="ru" selected>`, `name="expiry_date"`,
		"In Europe/Moscow time. A date alone means the end of that day.")
	noInline(t, body)
	friends := h.Subscription("Friends", 2)
	body = page(t, h, "/links/new")
	mustContain(t, body, "Choose a subscription")
	mustNotContain(t, body, "selected>Family")
	body = page(t, h, "/links/new?subscription="+strconv.FormatInt(friends, 10))
	mustContain(t, body, `<option value="2" selected>Friends`)
	if err := h.App.Settings.Set(h.T.Context(), "admin", "subscriptions", map[string]string{"subscriptions.link_language": "en"}); err != nil {
		t.Fatal(err)
	}
	mustContain(t, page(t, h, "/links/new"), `<option value="en" selected>`)

	// errors inline, nothing created
	rec := h.Login.Post("/links", url.Values{"name": {""}, "subscription": {"1"}, "language": {"en"}, "expiry": {"never"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Give it a name.") {
		t.Fatalf("empty name: %d", rec.Code)
	}
	rec = h.Login.Post("/links", url.Values{"name": {"Mom"}, "subscription": {"1"}, "language": {"en"}, "expiry": {"on"}, "expiry_date": {"2026-10-01"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "The expiry must be in the future.") || !strings.Contains(rec.Body.String(), `value="Mom"`) {
		t.Fatalf("past expiry: %d", rec.Code)
	}
	if list, _ := h.Mod.Links.List(h.T.Context(), links.Filter{State: "all"}); len(list) != 0 {
		t.Fatalf("%d links created", len(list))
	}
	rec = h.Login.Post("/links", url.Values{"name": {"Mom"}, "subscription": {strconv.FormatInt(family, 10)}, "language": {"ru"},
		"expiry": {"on"}, "expiry_date": {"2026-12-01"}, "note": {"her iPhone"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/links/1?created=1" {
		t.Fatalf("create: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body = page(t, h, "/links/1?created=1")
	mustContain(t, body, "Link created. Share the URL or let them scan the code.", "Family · until 1 Dec 2026 · Russian · created 7 Oct", "her iPhone")
}

func TestLinkPage(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1, 2)
	id, token := h.Link(sub, "Alex")
	for i := 0; i < 52; i++ {
		h.Advance(time.Minute)
		ua := "v2RayTun/2.1"
		if i == 51 {
			ua = "okhttp/4.12"
		}
		if rec := h.Fetch("GET", "/s/"+token, "[2001:db8:4f2::9]:4000", ua); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	href := "/links/" + strconv.FormatInt(id, 10)
	body := page(t, h, href)
	full := "http://proxier.test/s/" + token
	mustContain(t, body, "Alex", "Copy URL", `data-key="y"`, `data-copy="`+full+`"`,
		"http://proxier.test/s/"+strings.Repeat("•", 22), "The token is shown only here, never in notifications, events or logs.",
		"Show QR", "In the app: add subscription → scan QR.", "What the app gets now", "Built on every fetch. Nothing is cached.",
		"vless://••••••••@nl-1.hosts.tikhonnnnn.com", "profile-title: base64:", "Family", "nl-1, de-1",
		"no expiry", "English", "uri-plain (the subscription&#39;s default)", "just now · Other · 2001:db8:4f2::/48",
		"kept 90 days", "Other: okhttp/4.12", "v2RayTun", "2001:db8:4f2::9", "Older →",
		"Regenerate token…", "Change subscription…", "Set expiry…", "Edit…", "Delete…", "Disable…", "Created link Alex in Family")
	mustNotContain(t, body, substest.Credential(1), "<code>"+full)
	noInline(t, body)
	// phone order: the sharing parts first
	order := []string{`id="link-url"`, `id="link-qr"`, `aria-label="What the app gets now"`, `aria-label="Link"`, `aria-label="Fetch log"`, `aria-label="Actions"`, `aria-label="Activity"`}
	for i := 1; i < len(order); i++ {
		if strings.Index(body, order[i-1]) > strings.Index(body, order[i]) {
			t.Errorf("%s comes after %s", order[i-1], order[i])
		}
	}
	// 50 rows, and the older page holds the other 2
	if n := strings.Count(body, `class="row fetch"`); n != 50 {
		t.Errorf("%d fetch rows", n)
	}
	i := strings.Index(body, href+"?before=")
	older := body[i : i+strings.Index(body[i:], `"`)]
	older = strings.ReplaceAll(older, "&amp;", "&")
	if n := strings.Count(page(t, h, older), `class="row fetch"`); n != 2 {
		t.Errorf("older page: %d rows", n)
	}

	// Reveal and QR fragments, never cached
	rec := h.Login.Get(href+"/url", hx())
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "<code>"+full+"</code>") {
		t.Errorf("reveal: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.Login.Get(href+"/url?hide=1", hx()); strings.Contains(rec.Body.String(), "<code>"+full) {
		t.Error("hide still shows the URL")
	}
	rec = h.Login.Get(href+"/qr", hx())
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "<svg class=\"qr\"") || !strings.Contains(rec.Body.String(), "Hide QR") {
		t.Errorf("qr: %d", rec.Code)
	}
	if rec := h.Login.Get(href+"/qr?hide=1", hx()); strings.Contains(rec.Body.String(), "<svg") || !strings.Contains(rec.Body.String(), "Show QR") {
		t.Error("hide QR")
	}
	if rec := h.Login.Get("/links/99"); rec.Code != 404 {
		t.Errorf("unknown link: %d", rec.Code)
	}
}

func TestLinkActions(t *testing.T) {
	h := substest.New(t)
	family := h.Subscription("Family", 1, 2)
	h.Subscription("Friends", 2)
	id, token := h.Link(family, "Alex")
	href := "/links/" + strconv.FormatInt(id, 10)
	post := func(path string, form url.Values, wantTo string) {
		t.Helper()
		rec := h.Login.Post(path, form)
		if rec.Code != 303 || rec.Header().Get("Location") != wantTo {
			t.Fatalf("%s: %d %s %s", path, rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
	body := page(t, h, href)
	mustContain(t, body, "Disable Alex?", "Their app&#39;s list becomes the “disabled” entry on its next refresh.",
		"Give Alex a new URL?", "The old URL stops working at once. Apps holding it keep their last list. Send the new URL to Alex.",
		"Delete Alex?", "Its URL serves “⛔ Link removed” for the tombstone period")

	post(href+"/disable", nil, href)
	mustContain(t, page(t, h, href), "Enable", "Disabled: it serves “⛔ Link disabled”.")
	post(href+"/enable", nil, href)
	post(href+"/regenerate", nil, href+"?regenerated=1")
	fresh, _ := h.Mod.Links.Token(h.T.Context(), id)
	if fresh == token {
		t.Fatal("the token did not change")
	}
	mustContain(t, page(t, h, href+"?regenerated=1"), "New URL ready: copy it and send it.", `data-copy="http://proxier.test/s/`+fresh+`"`)

	mustContain(t, page(t, h, href+"/subscription"), "Change the subscription of Alex", `<option value="2">Friends`)
	post(href+"/subscription", url.Values{"subscription": {"2"}}, href)

	body = page(t, h, href+"/expiry")
	mustContain(t, body, "Expiry of Alex", `value="never" checked`)
	if rec := h.Login.Post(href+"/expiry", url.Values{"expiry": {"on"}, "expiry_date": {"2026-10-01"}}); rec.Code != 422 {
		t.Errorf("past expiry: %d", rec.Code)
	}
	post(href+"/expiry", url.Values{"expiry": {"on"}, "expiry_date": {"2026-12-01"}, "expiry_time": {"18:00"}}, href)
	body = page(t, h, href+"/expiry")
	mustContain(t, body, `value="on" checked`, `value="2026-12-01"`, `value="18:00"`, "Works until 1 Dec 2026, 18:00, Europe/Moscow time.")
	mustContain(t, page(t, h, href), "Friends · until 1 Dec 2026, 18:00 · English")

	mustContain(t, page(t, h, href+"/edit"), "Edit Alex", "The subscription&#39;s default (uri-plain)", `<option value="uri-base64">`)
	if rec := h.Login.Post(href+"/edit", url.Values{"name": {""}, "language": {"en"}}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "Give it a name.") {
		t.Errorf("empty name: %d", rec.Code)
	}
	post(href+"/edit", url.Values{"name": {"Alex — Pixel"}, "language": {"ru"}, "format": {"uri-base64"}, "note": {"new phone"}}, href)

	post(href+"/delete", nil, "/links")
	for typ, n := range map[string]int{
		"link.disabled": 1, "link.enabled": 1, "link.token_regenerated": 1, "link.subscription_changed": 1,
		"link.expiry_changed": 1, "link.changed": 1, "link.deleted": 1,
	} {
		ev := h.Events(typ)
		if len(ev) != n {
			t.Errorf("%s: %d events", typ, len(ev))
		}
		for _, e := range ev {
			if e.Actor != "admin" || e.Subject.String() != "link:1" {
				t.Errorf("%s: %+v", typ, e)
			}
		}
	}
	if ev := h.Events("link.changed"); len(ev) == 1 && ev[0].Payload["fields"] != "name, note, language, format" {
		t.Errorf("fields %v", ev[0].Payload["fields"])
	}
}

func TestDeletedLinkPage(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	id, token := h.Link(sub, "Mom")
	if err := h.Mod.Links.Delete(h.T.Context(), id, "admin"); err != nil {
		t.Fatal(err)
	}
	href := "/links/" + strconv.FormatInt(id, 10)
	body := page(t, h, href)
	mustContain(t, body, "Deleted on 7 Oct 2026. Its URL serves “⛔ Link removed” until 6 Nov 2026.", "deleted", "⛔")
	mustNotContain(t, body, token, "Copy URL", "Disable…", "Regenerate token…", "Delete…", "Show QR", "Change…", "Set…")
	for _, p := range []string{"/disable", "/enable", "/regenerate", "/delete", "/subscription", "/expiry", "/edit"} {
		if rec := h.Login.Post(href+p, url.Values{"subscription": {"1"}, "name": {"x"}, "language": {"en"}}); rec.Code != 409 {
			t.Errorf("POST %s: %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/url", "/qr", "/edit", "/expiry", "/subscription"} {
		if rec := h.Login.Get(href + p); rec.Code != 409 {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
}

func TestSubscriptionLinksArea(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Family", 1)
	other := h.Subscription("Friends", 2)
	mom, _ := h.Link(sub, "Mom")
	h.Link(sub, "Dad")
	h.Link(other, "Alex")
	gone, _ := h.Link(sub, "Old phone")
	if err := h.Mod.Links.Disable(h.T.Context(), mom, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Links.Delete(h.T.Context(), gone, "admin"); err != nil {
		t.Fatal(err)
	}
	body := page(t, h, "/subscriptions/"+strconv.FormatInt(sub, 10))
	mustContain(t, body, "Links · 2", `href="/links/1"`, `href="/links/2"`, "disabled", "never fetched",
		`href="/links/new?subscription=1"`, "2 links")
	mustNotContain(t, body, ">Alex<", "Old phone", "No links yet.")
}

func TestLinkListAlerts(t *testing.T) {
	h := substest.New(t)
	sub := h.Subscription("Friends", 1)
	alex, _ := h.Link(sub, "Alex")
	h.Link(sub, "Mom")
	shared(t, h, alex, 5)

	body := page(t, h, "/links")
	mustContain(t, body, "2 active · 0 expiring this week · 1 with an alert", "With alerts", `href="/links?alerts=1"`, ">Mom<", ">Alex<")
	if n := strings.Count(body, "looks shared"); n != 1 {
		t.Errorf("%d markers", n)
	}
	body = page(t, h, "/links?alerts=1")
	mustContain(t, body, ">Alex<", `aria-pressed="true"`, `name="alerts" value="1"`)
	mustNotContain(t, body, ">Mom<")

	// muted: no marker, not counted
	if err := h.Mod.Alerts.SetLimits(t.Context(), alex, 0, 0, true, "admin"); err != nil {
		t.Fatal(err)
	}
	body = page(t, h, "/links")
	mustNotContain(t, body, "looks shared", "with an alert")
}

package pages_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
)

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func mustNotContain(t *testing.T, body string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(body, n) {
			t.Errorf("unexpected %q", n)
		}
	}
}

var inlineScript = regexp.MustCompile(`<script(?:\s[^>]*)?>[^<]`)

func noInline(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, " style=") || strings.Contains(body, "<style") || inlineScript.MatchString(body) {
		t.Error("inline style or script")
	}
}

func page(t *testing.T, h *substest.Harness, path string) string {
	t.Helper()
	rec := h.Login.Get(path)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d", path, rec.Code)
	}
	return rec.Body.String()
}

func hx() http.Header { return http.Header{"Hx-Request": {"true"}} }

func TestSubscriptionList(t *testing.T) {
	h := substest.New(t)
	body := page(t, h, "/subscriptions")
	mustContain(t, body, "No subscriptions yet", "New subscription", `data-key="n"`)
	noInline(t, body)

	family := h.Subscription("Family", 1, 2)
	if _, err := h.Mod.Subs.Update(t.Context(), family, settings(h, family, func(v url.Values) { v.Set("title", "Семья"); v.Set("hide", "1") }), "admin"); err != nil {
		t.Fatal(err)
	}
	h.Subscription("Friends", 1)
	h.Catalog.SetHealth(2, "blocked", h.Now.Add(-time.Hour))
	h.Exec(`INSERT INTO subs_links (subscription_id, name, state, language, created_at) VALUES (?, 'Mom', 'active', 'ru', '2026-10-01T00:00:00.000Z')`, family)
	body = page(t, h, "/subscriptions")
	mustContain(t, body, "Family", "shown in apps as “Семья”", "Friends", "nl-1", "de-1", "st-blocked", "· de-1 hidden now",
		"uri-plain (default), uri-base64", "on · 30 min", "off", `data-href="/subscriptions/1"`)
	mustNotContain(t, body, "No subscriptions yet")
	noInline(t, body)
	// a member out of service: the gone marker
	h.Catalog.Drop(1)
	mustContain(t, page(t, h, "/subscriptions"), "st-gone")
}

// settings builds the settings form of id with change applied, through the service's view of it.
func settings(h *substest.Harness, id int64, change func(url.Values)) subsSettings {
	return formSettings(h, id, change)
}

func TestSubscriptionPage(t *testing.T) {
	h := substest.New(t)
	id := h.Subscription("Family", 1, 2)
	body := page(t, h, "/subscriptions/1")
	_ = id
	mustContain(t, body, "apps show “Family” · 2 servers · 0 links", "Delete…",
		"Servers · 2", "data-sortable", `data-sort-id="1"`, `data-sort-id="2"`, "data-sort-handle", `data-key="J"`, `data-key="K"`, "data-row-only",
		"data-sortable-form", `name="order" value="1,2"`,
		"Settings", `name="update_hours" value="12"`, "Save settings",
		"Preview", "200 OK", "profile-title: base64:RmFtaWx5", "vless://••••••••@nl-1.hosts.tikhonnnnn.com:443", "%2F••••••••",
		"Links · 0", "No links yet.", "Activity", "Created subscription Family", "Added nl-1", "Added de-1")
	mustNotContain(t, body, substest.Credential(1), substest.Path(1))
	noInline(t, body)
	// the first row can't go up, the last can't go down
	if !strings.Contains(body, `data-key="K" data-row-only disabled aria-label="Move nl-1 up"`) ||
		!strings.Contains(body, `data-key="J" data-row-only disabled aria-label="Move de-1 down"`) {
		t.Error("the ends are not disabled")
	}
	// every active server is in it: Add servers is disabled with the reason
	mustContain(t, body, `disabled title="Every active server is in it already."`)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	mustContain(t, page(t, h, "/subscriptions/1"), `href="/subscriptions/1/servers/add"`)
	if rec := h.Login.Get("/subscriptions/99"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
	// an empty one
	h.Subscription("Me")
	mustContain(t, page(t, h, "/subscriptions/2"), "No servers yet. Its links serve “⚠️ No servers yet”.", "#%E2%9A%A0%EF%B8%8F%20No%20servers%20yet")
}

func TestPreviewReveal(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Family", 1, 2)
	rec := h.Login.Get("/subscriptions/1/preview?format=uri-plain&reveal=1", hx())
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	mustContain(t, body, "vless://"+substest.Credential(1)+"@nl-1", "path=%2F"+strings.TrimPrefix(substest.Path(2), "/"))
	mustNotContain(t, body, "••••••••")
	// base64 revealed is the exact body; masked it shows the lines and says so
	rec = h.Login.Get("/subscriptions/1/preview?format=uri-base64&reveal=1", hx())
	mustNotContain(t, rec.Body.String(), "vless://")
	body = h.Login.Get("/subscriptions/1/preview?format=uri-base64", hx()).Body.String()
	mustContain(t, body, "vless://••••••••@", "Sent base64-encoded")
	if rec := h.Login.Get("/subscriptions/1/preview?format=mihomo", hx()); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad format: %d", rec.Code)
	}
	// the page itself never carries a credential
	mustNotContain(t, page(t, h, "/subscriptions/1"), substest.Credential(1), substest.Credential(2))
}

func TestServerListActions(t *testing.T) {
	h := substest.New(t)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	h.Subscription("Family", 1)

	body := page(t, h, "/subscriptions/1/servers/add")
	mustContain(t, body, "Add servers to Family", `value="2"`, `value="3"`, "🇩🇪", "🇫🇮")
	mustNotContain(t, body, `name="server" value="1"`)
	noInline(t, body)
	rec := h.Login.Post("/subscriptions/1/servers", url.Values{"server": {"3", "2"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/subscriptions/1" {
		t.Fatalf("add: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if got := order(t, h); got != "nl-1,de-1,fi-1" {
		t.Fatalf("order %s", got)
	}
	// a server gone meanwhile refuses the whole save
	h.Catalog.Put(substest.Server(4, "at-1", "🇦🇹", "Austria", 1))
	h.Catalog.Put(substest.Server(5, "se-1", "🇸🇪", "Sweden", 1))
	h.Catalog.Drop(5)
	if rec := h.Login.Post("/subscriptions/1/servers", url.Values{"server": {"4", "5"}}); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "a server you ticked is no longer active") {
		t.Fatalf("not served: %d", rec.Code)
	}

	// move with htmx answers the area with the cursor on the moved row
	rec = h.Login.Site.Do(post(h, "/subscriptions/1/servers/3/move", url.Values{"dir": {"up"}}))
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), `<section class="area" id="servers-area"`) {
		t.Fatalf("move: %d %.80s", rec.Code, rec.Body.String())
	}
	mustContain(t, rec.Body.String(), `data-sort-id="3" data-hint="J K move fi-1 down · up" data-href="/servers/3" data-cursor`)
	if got := order(t, h); got != "nl-1,fi-1,de-1" {
		t.Fatalf("after move: %s", got)
	}
	// without htmx: a redirect
	if rec := h.Login.Post("/subscriptions/1/servers/3/move", url.Values{"dir": {"down"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("plain move: %d", rec.Code)
	}
	// the drag's order
	rec = h.Login.Site.Do(post(h, "/subscriptions/1/servers/order", url.Values{"order": {"2,3,1"}}))
	if rec.Code != 200 || order(t, h) != "de-1,fi-1,nl-1" {
		t.Fatalf("order: %d %s", rec.Code, order(t, h))
	}
	rec = h.Login.Site.Do(post(h, "/subscriptions/1/servers/order", url.Values{"order": {"2,1"}}))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "The servers changed meanwhile") || order(t, h) != "de-1,fi-1,nl-1" {
		t.Errorf("stale order: %d", rec.Code)
	}
	if rec := h.Login.Post("/subscriptions/1/servers/order", url.Values{"order": {"x"}}); rec.Code != http.StatusConflict {
		t.Errorf("plain stale order: %d", rec.Code)
	}
	// remove
	if rec := h.Login.Post("/subscriptions/1/servers/3/remove", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("remove: %d", rec.Code)
	}
	if got := order(t, h); got != "de-1,nl-1" {
		t.Errorf("after remove: %s", got)
	}
	if rec := h.Login.Post("/subscriptions/1/servers/3/remove", nil); rec.Code != http.StatusNotFound {
		t.Errorf("remove again: %d", rec.Code)
	}
}

func TestSettingsFormErrors(t *testing.T) {
	h := substest.New(t)
	h.Subscription("Family", 1)
	h.Subscription("Friends")
	form := formValues(h, 1)
	form.Set("name", "Friends")
	form.Set("title", "Семья typed")
	form["format"] = []string{"uri-base64"}
	form.Set("update_hours", "0")
	form.Set("grace", "soon")
	rec := h.Login.Post("/subscriptions/1", form)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("errors: %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "Nothing was saved", "Another subscription has this name.", "Choose another default first",
		"Whole hours, 1 to 168.", "Whole minutes, 0 to 10,080.",
		`value="Friends"`, `value="Семья typed"`, `name="grace" value="soon"`, `value="uri-base64" checked`)
	mustNotContain(t, body, `value="uri-plain" checked>uri-plain`)
	noInline(t, body)
	if n := len(h.Events("subscription.updated")); n != 0 {
		t.Errorf("%d updates recorded", n)
	}
	// a good save, then the same again
	form = formValues(h, 1)
	form.Set("title", "Семья")
	if rec := h.Login.Post("/subscriptions/1", form); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/subscriptions/1?saved=1" {
		t.Fatalf("save: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	mustContain(t, page(t, h, "/subscriptions/1?saved=1"), "Saved.", "apps show “Семья”")
	if rec := h.Login.Post("/subscriptions/1", form); rec.Header().Get("Location") != "/subscriptions/1?saved=0" {
		t.Errorf("unchanged: %s", rec.Header().Get("Location"))
	}
	mustContain(t, page(t, h, "/subscriptions/1?saved=0"), "No changes.")
}

func TestCreateAndDeletePages(t *testing.T) {
	h := substest.New(t)
	body := page(t, h, "/subscriptions/new")
	mustContain(t, body, "New subscription", `name="name"`, `name="title"`, `name="description"`)
	noInline(t, body)
	if rec := h.Login.Post("/subscriptions", url.Values{"name": {""}}); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "Give it a name.") {
		t.Fatalf("empty name: %d", rec.Code)
	}
	rec := h.Login.Post("/subscriptions", url.Values{"name": {"Family"}, "title": {""}, "description": {"for the family"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/subscriptions/1" {
		t.Fatalf("create: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if err := h.Mod.Subs.AddServers(t.Context(), 1, []int64{1, 2}, "admin"); err != nil {
		t.Fatal(err)
	}

	h.Exec(`INSERT INTO subs_links (subscription_id, name, state, language, created_at) VALUES (1, 'Mom', 'active', 'ru', '2026-10-01T00:00:00.000Z')`)
	h.Exec(`INSERT INTO subs_links (subscription_id, name, state, language, created_at, deleted_at) VALUES (1, 'Dad', 'deleted', 'ru', '2026-09-01T00:00:00.000Z', '2026-10-01T00:00:00.000Z')`)
	body = page(t, h, "/subscriptions/1/delete")
	mustContain(t, body, "Delete Family?", "Links still point to it", "Mom", "Dad: keeps it until 31 Oct 2026", `class="pc-btn pc-dng" disabled`)
	noInline(t, body)
	if rec := h.Login.Post("/subscriptions/1/delete", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Not deleted") {
		t.Fatalf("refused: %d", rec.Code)
	}
	h.Exec(`DELETE FROM subs_links`)
	body = page(t, h, "/subscriptions/1/delete")
	mustContain(t, body, "Family and its order of 2 servers are deleted. No link uses it.")
	mustNotContain(t, body, "disabled>Delete")
	if rec := h.Login.Post("/subscriptions/1/delete", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/subscriptions" {
		t.Fatalf("delete: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := h.Login.Get("/subscriptions/1"); rec.Code != http.StatusNotFound {
		t.Errorf("after delete: %d", rec.Code)
	}
}

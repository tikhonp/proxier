package pages_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

// listOrder is a list's services' tags in order.
func listOrder(t *testing.T, h *routingtest.Harness, id int64) string {
	t.Helper()
	v, err := h.Mod.Lists.View(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, m := range v.Members {
		tags = append(tags, m.Service.Tag)
	}
	return strings.Join(tags, ",")
}

func TestListPages(t *testing.T) {
	h := routingtest.New(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	anthropic := h.Upstream("anthropic")
	mine := h.Custom("mine", "claude.ai", "example.com")
	main := h.List("Main", anthropic, mine)
	h.List("Parents")
	// a refresh (3c) brings a name that covers nl-1: left out, with a warning
	h.Exec(`UPDATE routing_snapshots SET suffix = 'anthropic.com' || char(10) || 'claude.ai' || char(10) || 'tikhonnnnn.com', suffix_count = 3
		WHERE service_id = ? AND status = 'accepted'`, anthropic)

	body := h.Login.Get("/routing/lists").Body.String()
	has(t, "lists", body, "Routing lists", "A list is an ordered set of services.", `href="/routing/lists/new"`, `data-key="n"`,
		">Main<", "default for new targets", ">Parents<", `href="/routing/lists/1"`, "▲ 1 warning", ">none<", "2 lists",
		">List<", ">Services<", ">Domains<", ">Targets<", ">Warnings<")

	body = h.Login.Get(fmt.Sprintf("/routing/lists/%d", main)).Body.String()
	has(t, "list page", body, "<h1>Main</h1>", "2 services · 3 domains · default for new targets", `href="/routing/lists/1/add"`,
		"Services, in order", "a domain belongs to the first service that has it", "data-sortable", fmt.Sprintf(`data-sort-id="%d"`, anthropic),
		fmt.Sprintf(`data-sort-id="%d"`, mine), "data-sort-handle", `data-key="J"`, `data-key="K"`, "data-row-only", "data-sortable-form",
		fmt.Sprintf(`name="order" value="%d,%d"`, anthropic, mine), "v2fly:anthropic",
		"Targets · 0", "No routers or Shadowrocket configs follow Main yet.",
		"Warnings · 1", "v2fly:anthropic has tikhonnnnn.com, which covers nl-1.hosts.tikhonnnnn.com (nl-1): left out of what targets get.",
		"Actions", "Rename…", "Delete…", "The default list can&#39;t be deleted.", "Activity", "Added anthropic", "Added mine")
	// owned / total: anthropic installs 2 of 3, mine 1 of 2
	has(t, "owned/total", body, `2 <span class="subtle">/ 3</span>`, `1 <span class="subtle">/ 2</span>`)
	if strings.Contains(body, `action="/routing/lists/1/default"`) {
		t.Error("Make default on the default")
	}
	parents := h.Login.Get("/routing/lists/2").Body.String()
	has(t, "empty list", parents, "No services yet. Targets following Parents get nothing from Proxier.", `action="/routing/lists/2/default"`)

	// new list and rename
	has(t, "new", h.Login.Get("/routing/lists/new").Body.String(), "New routing list", `name="name"`, `name="description"`)
	if rec := h.Login.Post("/routing/lists", url.Values{"name": {"Main"}}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "Another list has this name.") {
		t.Errorf("taken: %d", rec.Code)
	}
	rec := h.Login.Post("/routing/lists", url.Values{"name": {"Office"}, "description": {"the office router"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/routing/lists/3" {
		t.Fatalf("create: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	has(t, "edit", h.Login.Get("/routing/lists/3/edit").Body.String(), "Rename Office", `value="Office"`, "the office router")
	if rec := h.Login.Post("/routing/lists/3/edit", url.Values{"name": {"Work"}, "description": {""}}); rec.Code != 303 {
		t.Errorf("rename: %d", rec.Code)
	}
	if rec := h.Login.Post("/routing/lists/3/default", nil); rec.Code != 303 {
		t.Errorf("default: %d", rec.Code)
	}
	has(t, "renamed default", h.Login.Get("/routing/lists/3").Body.String(), "<h1>Work</h1>", "default for new targets", "Made Work the default")
	if rec := h.Login.Get("/routing/lists/99"); rec.Code != 404 {
		t.Errorf("missing list: %d", rec.Code)
	}
	// subjects link to the list
	has(t, "activity", h.Login.Get("/activity?subject=routing_list:3").Body.String(), `href="/routing/lists/3"`)
}

func TestListActions(t *testing.T) {
	h := routingtest.New(t)
	a := h.Custom("a", "a.com")
	b := h.Custom("b", "b.com")
	c := h.Custom("c", "c.com")
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	bad := h.Custom("bad", "tikhonnnnn.com")

	// the picker: services not in the list, filtered
	body := h.Login.Get("/routing/lists/1/add").Body.String()
	has(t, "picker", body, "Add services to Main", `name="service"`, ">a<", ">b<", ">c<", ">bad<", "Or add a new one", `name="lists" value="1"`)
	body = h.Login.Get("/routing/lists/1/add?q=ba").Body.String()
	if !strings.Contains(body, ">bad<") || strings.Contains(body, `value="1"/><span class="col"><span class="bold">a<`) || strings.Contains(body, ">c<") {
		t.Errorf("filter: %s", body)
	}
	rec := h.Login.Post("/routing/lists/1/services", url.Values{"service": {fmt.Sprint(c), fmt.Sprint(a), fmt.Sprint(b)}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/routing/lists/1?added=3" {
		t.Fatalf("add: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if got := listOrder(t, h, 1); got != "a,b,c" {
		t.Fatalf("order %s", got)
	}
	has(t, "band", h.Login.Get("/routing/lists/1?added=3").Body.String(), "Added 3 services.")
	// the guard refuses on the picker, keeping the ticks
	rec = h.Login.Post("/routing/lists/1/services", url.Values{"service": {fmt.Sprint(bad)}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Adding tikhonnnnn.com would cover nl-1.hosts.tikhonnnnn.com, the hostname of nl-1. It wasn&#39;t saved.") ||
		!strings.Contains(rec.Body.String(), fmt.Sprintf(`value="%d" checked`, bad)) {
		t.Errorf("guard: %d %s", rec.Code, rec.Body)
	}

	// move (J/K) under htmx: the area with the cursor on the moved row
	area := hx(h, "POST", fmt.Sprintf("/routing/lists/1/services/%d/move", c), url.Values{"dir": {"up"}})
	has(t, "move", area, `id="members-area"`, fmt.Sprintf(`data-sort-id="%d" data-hint="J K move c down · up" data-href="/routing/services/%d" data-cursor`, c, c))
	if got := listOrder(t, h, 1); got != "a,c,b" {
		t.Fatalf("after move: %s", got)
	}
	// without htmx: a redirect
	if rec := h.Login.Post(fmt.Sprintf("/routing/lists/1/services/%d/move", c), url.Values{"dir": {"down"}}); rec.Code != 303 {
		t.Errorf("plain move: %d", rec.Code)
	}
	// the drag's order
	hx(h, "POST", "/routing/lists/1/services/order", url.Values{"order": {fmt.Sprintf("%d,%d,%d", c, b, a)}})
	if got := listOrder(t, h, 1); got != "c,b,a" {
		t.Fatalf("after order: %s", got)
	}
	// stale: 200 with the notice under htmx, 409 without
	stale := hx(h, "POST", "/routing/lists/1/services/order", url.Values{"order": {fmt.Sprintf("%d,%d", a, b)}})
	has(t, "stale", stale, "The services changed meanwhile")
	if got := listOrder(t, h, 1); got != "c,b,a" {
		t.Errorf("a stale order changed it: %s", got)
	}
	if rec := h.Login.Post("/routing/lists/1/services/order", url.Values{"order": {"x"}}); rec.Code != http.StatusConflict {
		t.Errorf("plain stale: %d", rec.Code)
	}
	// remove
	if rec := h.Login.Post(fmt.Sprintf("/routing/lists/1/services/%d/remove", b), nil); rec.Code != 303 {
		t.Errorf("remove: %d", rec.Code)
	}
	if got := listOrder(t, h, 1); got != "c,a" {
		t.Errorf("after remove: %s", got)
	}
	has(t, "remove twice", hx(h, "POST", fmt.Sprintf("/routing/lists/1/services/%d/remove", b), url.Values{}), "The services changed meanwhile")

	// a service's Add to lists
	h.List("Parents")
	body = h.Login.Get(fmt.Sprintf("/routing/services/%d/lists", b)).Body.String()
	has(t, "to lists", body, "Add b to lists", ">Main<", ">Parents<", "in no list")
	if rec := h.Login.Post(fmt.Sprintf("/routing/services/%d/lists", b), url.Values{"list": {"2"}}); rec.Code != 303 {
		t.Errorf("to lists: %d", rec.Code)
	}
	if got := listOrder(t, h, 2); got != "b" {
		t.Errorf("Parents: %s", got)
	}
	if rec := h.Login.Post(fmt.Sprintf("/routing/services/%d/lists", bad), url.Values{"list": {"2"}}); rec.Code != 422 {
		t.Errorf("to lists, guarded: %d", rec.Code)
	}
}

func TestUndoReorder(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("youtube", "youtube.com\ngooglevideo.com\nytimg.com\n")
	h.Up.V2fly("google", "google.com\ngooglevideo.com\nytimg.com\n")
	yt := h.Upstream("youtube")
	g := h.Upstream("google")
	h.List("Main", g, yt)
	h.Exec(`INSERT INTO routing_routers (name, list_id, state, host, ssh_user, created_by, created_at)
		VALUES ('Home', 1, 'active', '192.168.88.1', 'proxier', 'admin', '2026-10-08T12:00:00.000Z')`)
	updated := func() int { return len(h.Events("routing.list_updated")) }
	n := updated()

	area := hx(h, "POST", fmt.Sprintf("/routing/lists/1/services/%d/move", yt), url.Values{"dir": {"up"}})
	has(t, "band", area, "Moved youtube above google", "2 domains change owner (googlevideo.com, ytimg.com).", "Home re-sync.",
		`data-key="u"`, ">Undo", fmt.Sprintf(`name="order" value="%d,%d"`, g, yt), fmt.Sprintf(`name="expect" value="%d,%d"`, yt, g))
	if listOrder(t, h, 1) != "youtube,google" || updated() != n+1 {
		t.Fatalf("move: %s", listOrder(t, h, 1))
	}

	// Undo puts it back, and offers Undo again
	area = hx(h, "POST", "/routing/lists/1/services/order", url.Values{"order": {fmt.Sprintf("%d,%d", g, yt)}, "expect": {fmt.Sprintf("%d,%d", yt, g)}})
	has(t, "undone", area, "Moved google above youtube", "2 domains change owner", ">Undo")
	if listOrder(t, h, 1) != "google,youtube" || updated() != n+2 {
		t.Fatalf("undo: %s, %d events", listOrder(t, h, 1), updated()-n)
	}

	// an Undo after another change is stale
	hx(h, "POST", fmt.Sprintf("/routing/lists/1/services/%d/move", g), url.Values{"dir": {"down"}}) // youtube,google
	hx(h, "POST", fmt.Sprintf("/routing/lists/1/services/%d/move", g), url.Values{"dir": {"up"}})   // google,youtube
	stale := hx(h, "POST", "/routing/lists/1/services/order", url.Values{"order": {fmt.Sprintf("%d,%d", g, yt)}, "expect": {fmt.Sprintf("%d,%d", yt, g)}})
	has(t, "stale undo", stale, "The services changed meanwhile")
	if strings.Contains(stale, ">Undo") {
		t.Error("a stale Undo offers Undo")
	}

	// a reorder that moves no name says so
	a := h.Custom("a", "a.com")
	h.List("Main", a)
	area = hx(h, "POST", fmt.Sprintf("/routing/lists/1/services/%d/move", a), url.Values{"dir": {"up"}})
	has(t, "no owner", area, "Moved a above youtube", "No domain changes owner.")
}

func TestDeleteListPage(t *testing.T) {
	h := routingtest.New(t)
	body := h.Login.Get("/routing/lists/1/delete").Body.String()
	has(t, "default", body, "Delete Main", "The default list can&#39;t be deleted. Make another list the default first.")
	if strings.Contains(body, `action="/routing/lists/1/delete"`) {
		t.Error("the default offers Delete")
	}
	if rec := h.Login.Post("/routing/lists/1/delete", nil); rec.Code != http.StatusConflict {
		t.Errorf("delete the default: %d", rec.Code)
	}

	mine := h.Custom("mine", "example.com")
	parents := h.List("Parents", mine)
	h.Exec(`INSERT INTO routing_routers (name, list_id, state, host, ssh_user, created_by, created_at)
		VALUES ('Home', ?, 'active', '192.168.88.1', 'proxier', 'admin', '2026-10-08T12:00:00.000Z')`, parents)
	path := fmt.Sprintf("/routing/lists/%d/delete", parents)
	body = h.Login.Get(path).Body.String()
	has(t, "targets", body, "1 target follows Parents. Move it to another list in the same step.", ">Home<", `name="move_to"`,
		`<option value="1" selected>Main</option>`, "Move and delete Parents")
	if rec := h.Login.Post(path, nil); rec.Code != http.StatusConflict {
		t.Errorf("without move_to: %d", rec.Code)
	}
	rec := h.Login.Post(path, url.Values{"move_to": {"1"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/routing/lists" {
		t.Fatalf("move and delete: %d", rec.Code)
	}
	has(t, "Main's targets", h.Login.Get("/routing/lists/1").Body.String(), "Targets · 1", ">Home<")

	other := h.List("Other", mine)
	path = fmt.Sprintf("/routing/lists/%d/delete", other)
	has(t, "plain", h.Login.Get(path).Body.String(), "Delete Other? Its 1 service stays; only the list goes.", "Delete Other")
	if rec := h.Login.Post(path, nil); rec.Code != 303 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := h.Login.Get(fmt.Sprintf("/routing/lists/%d", other)); rec.Code != 404 {
		t.Errorf("deleted list: %d", rec.Code)
	}
}

func TestOwnershipOnServicePages(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Parents")

	// Add service: the default list is ticked
	body := h.Login.Get("/routing/services/add").Body.String()
	has(t, "add form", body, "Add to routing lists", `name="lists" value="1" checked`, `name="lists" value="2">`)
	rec := h.Login.Post("/routing/services", url.Values{"selector": {"anthropic"}, "lists": {"1"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/routing/services/1?added=1" {
		t.Fatalf("add: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if got := listOrder(t, h, 1); got != "anthropic" {
		t.Fatalf("Main: %s", got)
	}
	// from a list's picker: back to the list
	rec = h.Login.Post("/routing/services", url.Values{"selector": {"openai"}, "lists": {"2"}, "from_list": {"1"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/routing/lists/2?added=1" {
		t.Fatalf("add from a list: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	mine := h.Custom("mine", "claude.ai", "full:console.anthropic.com", "example.com", "full:api.openai.com")
	h.List("Main", mine)
	h.List("Parents", mine)
	body = h.Login.Get(fmt.Sprintf("/routing/services/%d", mine)).Body.String()
	has(t, "service page", body, "in Main and Parents", "Main #2 · Parents #2", "Not installed in Main: 2 names",
		"owned by anthropic (first in Main)", "covered by anthropic.com (anthropic)", "Not installed in Parents: 1 name",
		"covered by openai.com (openai)", fmt.Sprintf(`href="/routing/services/%d/lists"`, mine), "Add to lists…")

	// the editor's hints and footer
	body = h.Login.Get(fmt.Sprintf("/routing/services/%d/edit", mine)).Body.String()
	has(t, "editor", body, "covered by anthropic in Main", "covered by openai in Parents", "Saving changes what the targets of Main and Parents get.")
	if strings.Count(body, "cover-hint") != 3 {
		t.Errorf("%d hints", strings.Count(body, "cover-hint"))
	}

	// a guard refusal shows above the table and keeps the edits
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	rec = h.Login.Post(fmt.Sprintf("/routing/services/%d/edit", mine), editForm("example.com", "tikhonnnnn.com"))
	if rec.Code != 422 {
		t.Fatalf("guarded save: %d", rec.Code)
	}
	has(t, "guarded save", rec.Body.String(), "Adding tikhonnnnn.com would cover nl-1.hosts.tikhonnnnn.com, the hostname of nl-1.", `value="tikhonnnnn.com"`)

	// the remove refusal links the lists
	has(t, "remove", h.Login.Get(fmt.Sprintf("/routing/services/%d/remove", mine)).Body.String(), `href="/routing/lists/1"`, `href="/routing/lists/2"`)

	// the services list: the Lists column and its filters
	body = h.Login.Get("/routing/services").Body.String()
	has(t, "services list", body, "Main, Parents", "in Main", "in Parents", "Not in any list · 0")
	h.Custom("free")
	body = h.Login.Get("/routing/services?list=none").Body.String()
	has(t, "no list", body, ">free<", "Not in any list · 1")
	if strings.Contains(body, ">mine<") {
		t.Error("the none filter shows mine")
	}
	body = h.Login.Get("/routing/services?list=2").Body.String()
	if !strings.Contains(body, ">openai<") || strings.Contains(body, ">anthropic<") {
		t.Error("the list filter")
	}
}

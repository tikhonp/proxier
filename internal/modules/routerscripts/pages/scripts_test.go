package pages_test

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var ctx = context.Background()

// checkNoInline fails on inline script, style or event handlers (the CSP).
func checkNoInline(t *testing.T, name, page string) {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			if n.Data == "style" {
				t.Errorf("%s: <style> element", name)
			}
			if n.Data == "script" && attr(n, "src") == "" {
				t.Errorf("%s: inline <script>", name)
			}
			for _, a := range n.Attr {
				if a.Key == "style" || strings.HasPrefix(a.Key, "on") || strings.HasPrefix(a.Key, "hx-on") {
					t.Errorf("%s: <%s %s=…>", name, n.Data, a.Key)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// textarea is the text the browser shows in the text area with that id
// (the parser drops the first newline, as browsers do).
func textarea(t *testing.T, page, id string) (string, *xhtml.Node) {
	t.Helper()
	doc, _ := xhtml.Parse(strings.NewReader(page))
	var found *xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "textarea" && attr(n, "id") == id {
			found = n
		}
		for c := n.FirstChild; c != nil && found == nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if found == nil {
		t.Fatalf("no textarea #%s", id)
	}
	var b strings.Builder
	for c := found.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(c.Data)
	}
	return b.String(), found
}

func countEvents(t *testing.T, h *rscriptstest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM events`); err != nil {
		t.Fatal(err)
	}
	return n
}

func create(t *testing.T, h *rscriptstest.Harness, name, body string) int64 {
	t.Helper()
	id, err := h.Mod.Scripts.Create(ctx, scripts.New{Name: name, Body: body}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestScriptsList(t *testing.T) {
	h := rscriptstest.New(t)
	empty := h.Login.Get("/router-scripts").Body.String()
	if !strings.Contains(empty, "A router script is a RouterOS file") || !strings.Contains(empty, `data-action="rs.new"`) {
		t.Errorf("empty state:\n%s", empty)
	}
	end := paramstest.WithEnd(paramstest.Today())
	h.Script("fresh-router", end)
	h.Exec(`UPDATE rscripts_scripts SET description = 'bootstrap a MikroTik' WHERE id = 1`)
	if _, err := h.Mod.Scripts.EditDraft(ctx, 1, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Exec(`INSERT INTO rscripts_generations (script_id, version, router_name, file_name, created_at, created_by) VALUES (1, 1, 'Dacha', 'x', '2026-10-09T12:00:00.000Z', 'admin')`)
	create(t, h, "broken", string(paramstest.Replace(paramstest.Annotate(end, "subUrl", "@fil x"), ":local wanIface \"ether1\"\n", ":local wanIface \"ether1\"\n:local lanNet \"1\"\n")))
	old := h.Script("old", []byte("# none\n"))
	if err := h.Mod.Scripts.Archive(ctx, old, true, "admin"); err != nil {
		t.Fatal(err)
	}

	page := h.Login.Get("/router-scripts").Body.String()
	checkNoInline(t, "list", page)
	for _, want := range []string{">fresh-router<", "bootstrap a MikroTik", ">v1<", "9 Oct 2026", "draft · 18 parameters", ">broken<",
		"no version yet", "draft · 2 problems", "Show archived (1)", `data-href="/router-scripts/1"`, "RouterOS scripts that set up a fresh MikroTik"} {
		if !strings.Contains(page, want) {
			t.Errorf("list has no %q", want)
		}
	}
	if strings.Contains(page, ">old<") {
		t.Error("the archived script is listed")
	}
	arch := h.Login.Get("/router-scripts?archived=1").Body.String()
	if !strings.Contains(arch, ">old<") || strings.Contains(arch, ">fresh-router<") || !strings.Contains(arch, "no draft") {
		t.Errorf("archived list:\n%s", arch)
	}
}

func TestScriptPage(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.WithEnd(paramstest.Today()))
	// without a draft: Edit
	page := h.Login.Get("/router-scripts/1").Body.String()
	checkNoInline(t, "no draft", page)
	for _, want := range []string{`data-action="rs.edit"`, "No draft. Edit makes one from v1, the current version.", "current v1 · 0 generations",
		">Versions<", "v1", "· current", ">Generations<", "None yet.", ">Actions<", "Edit details", "Archive", "Delete…", ">Activity<", "Published fresh-router v1"} {
		if !strings.Contains(page, want) {
			t.Errorf("no draft: no %q", want)
		}
	}
	if strings.Contains(page, `data-code="routeros"`) || strings.Contains(page, "Discard draft") {
		t.Error("an editor without a draft")
	}
	res := h.Login.Post("/router-scripts/1/edit", nil)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", res.Code)
	}
	page = h.Login.Get("/router-scripts/1").Body.String()
	checkNoInline(t, "draft", page)
	_, ta := textarea(t, page, "draft-body")
	for k, want := range map[string]string{
		"data-code": "routeros", "name": "body", "hx-post": "/router-scripts/1/draft/parameters", "hx-trigger": "input changed delay:400ms",
		"hx-target": "#params-panel", "hx-swap": "outerHTML",
	} {
		if got := attr(ta, k); got != want {
			t.Errorf("textarea %s=%q", k, got)
		}
	}
	for _, want := range []string{`name="revision" value="1"`, `id="params-panel"`, `data-code-findings="draft-body"`, "Detected parameters · 18",
		"Publish v2…", "js/editor.bundle.js", `data-code-save`, `form="draft-form"`, "draft based on v1", "Discard draft…", "The draft is dropped; v1 stays current.",
		"interface names", "vpnGateway", "→ 192.168.89.2", `&#34;10.230.1&#34;`, "Saved the draft of fresh-router"} {
		if !strings.Contains(page, want) {
			t.Errorf("draft: no %q", want)
		}
	}
	if strings.Contains(page, `data-action="rs.edit"`) {
		t.Error("Edit next to a draft")
	}
	// a new script with no version: no Discard
	create(t, h, "new", "x")
	page = h.Login.Get("/router-scripts/2").Body.String()
	if strings.Contains(page, "Discard draft") || !strings.Contains(page, "no version yet · draft") || !strings.Contains(page, "Publish v1…") {
		t.Errorf("new script:\n%s", page)
	}
	if h.Login.Get("/router-scripts/99").Code != http.StatusNotFound {
		t.Error("a missing script")
	}
	_ = id
}

func TestParametersPanel(t *testing.T) {
	h := rscriptstest.New(t)
	create(t, h, "fresh-router", "x")
	before := countEvents(t, h)
	body := string(paramstest.Replace(paramstest.Annotate(paramstest.WithEnd(paramstest.Today()), "subUrl", "@fill subscription-link"),
		":local wanIface \"ether1\"\n", ":local wanIface \"ether1\"\n:local lanNet \"1\"\n"))
	body = string(paramstest.Annotate([]byte(body), "dohIP", "@secret"))
	res := h.Site.Do(sitetest.Req{Method: http.MethodPost, Path: "/router-scripts/1/draft/parameters",
		Form: url.Values{"_csrf": {h.Login.CSRF}, "body": {body}}, Cookies: []*http.Cookie{h.Login.Cookie}, Header: http.Header{"Hx-Request": {"true"}}})
	if res.Code != http.StatusOK {
		t.Fatalf("panel: %d", res.Code)
	}
	panel := res.Body.String()
	if strings.Contains(panel, "<html") {
		t.Error("the panel is a whole page")
	}
	for _, want := range []string{`id="params-panel"`, `data-code-findings="draft-body"`, "Detected parameters · 18", ">subUrl<", "@fill subscription-link",
		">dohIP<", "•••", ">vpnGateway<", `= ($containerNet . &#34;.2&#34;)`, "→ 192.168.89.2", "interface names",
		`data-code-finding`, `data-line="27"`, `data-sev="error"`, "Duplicate parameter lanNet."} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel has no %q", want)
		}
	}
	if strings.Contains(panel, "8.8.8.8") {
		t.Error("a secret default is shown")
	}
	if n := countEvents(t, h); n != before {
		t.Errorf("the panel recorded %d events", n-before)
	}
	// without a block
	res = h.Site.Do(sitetest.Req{Method: http.MethodPost, Path: "/router-scripts/1/draft/parameters",
		Form: url.Values{"_csrf": {h.Login.CSRF}, "body": {"/system identity set name=x\n"}}, Cookies: []*http.Cookie{h.Login.Cookie}})
	if !strings.Contains(res.Body.String(), "No PARAMETERS block: the script is versioned and downloadable, with nothing to fill in.") {
		t.Errorf("no block:\n%s", res.Body.String())
	}
	// against the base version: the gone parameters
	end := paramstest.WithEnd(paramstest.Today())
	h.Script("v", end)
	if _, err := h.Mod.Scripts.EditDraft(ctx, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	res = h.Site.Do(sitetest.Req{Method: http.MethodPost, Path: "/router-scripts/2/draft/parameters",
		Form: url.Values{"_csrf": {h.Login.CSRF}, "body": {string(paramstest.Replace(end, ":local vethName \"vless\"\n", ""))}}, Cookies: []*http.Cookie{h.Login.Cookie}})
	if !strings.Contains(res.Body.String(), "vethName was in v1 and is gone here.") {
		t.Errorf("gone:\n%s", res.Body.String())
	}
}

func TestDraftRoundTripsThroughTheForm(t *testing.T) {
	h := rscriptstest.New(t)
	body := "\n# PARAMETERS\n:local a \"<b> & \\\"c\\\"\"\n# END PARAMETERS\n:put \"</textarea><script>x</script>\"\n"
	res := h.Login.PostMultipart("/router-scripts", url.Values{"name": {"tricky"}, "body": {body}}, nil)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	if d, _ := h.Mod.Scripts.Draft(ctx, 1); d.Body != body {
		t.Fatalf("stored: %q", d.Body)
	}
	page := h.Login.Get("/router-scripts/1").Body.String()
	shown, _ := textarea(t, page, "draft-body")
	if shown != body {
		t.Fatalf("shown: %q", shown)
	}
	if strings.Contains(page, "</textarea><script>") {
		t.Error("the body broke out of the text area")
	}
	// the browser posts it back with CRLF; nothing changed
	res = h.Login.PostMultipart("/router-scripts/1/draft", url.Values{"revision": {"1"}, "body": {strings.ReplaceAll(shown, "\n", "\r\n")}}, nil)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", res.Code)
	}
	if d, _ := h.Mod.Scripts.Draft(ctx, 1); d.Body != body || d.Revision != 1 {
		t.Errorf("after the round trip: %q revision %d", d.Body, d.Revision)
	}
	// next=publish saves and opens the publish page
	res = h.Login.PostMultipart("/router-scripts/1/draft", url.Values{"revision": {"1"}, "body": {shown + "# more\n"}, "next": {"publish"}}, nil)
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/router-scripts/1/publish" {
		t.Errorf("save and publish: %d %s", res.Code, res.Header().Get("Location"))
	}
	if d, _ := h.Mod.Scripts.Draft(ctx, 1); d.Body != body+"# more\n" || d.Revision != 2 {
		t.Errorf("saved before publish: revision %d", d.Revision)
	}
}

func TestStaleDraftBand(t *testing.T) {
	h := rscriptstest.New(t)
	create(t, h, "s", "a\n")
	if _, err := h.Mod.Scripts.SaveDraft(ctx, 1, 1, "from another tab\n", "admin"); err != nil {
		t.Fatal(err)
	}
	res := h.Login.PostMultipart("/router-scripts/1/draft", url.Values{"revision": {"1"}, "body": {"my text\r\n"}}, nil)
	if res.Code != http.StatusConflict {
		t.Fatalf("stale: %d", res.Code)
	}
	page := res.Body.String()
	if !strings.Contains(page, "The draft changed since you opened it (another tab saved it).") || !strings.Contains(page, `data-action="rs.reload"`) {
		t.Errorf("no band:\n%s", page)
	}
	if shown, _ := textarea(t, page, "draft-body"); shown != "my text\n" {
		t.Errorf("the posted text: %q", shown)
	}
	for _, act := range []string{"rs.save", "rs.publish"} {
		i := strings.Index(page, `data-action="`+act+`"`)
		tag := page[strings.LastIndex(page[:i], "<"):]
		tag = tag[:strings.Index(tag, ">")]
		if !strings.Contains(tag, "disabled") {
			t.Errorf("%s is on: %s", act, tag)
		}
	}
	if d, _ := h.Mod.Scripts.Draft(ctx, 1); d.Body != "from another tab\n" || d.Revision != 2 {
		t.Errorf("draft: %+v", d)
	}
}

func TestPublishPage(t *testing.T) {
	h := rscriptstest.New(t)
	create(t, h, "fresh-router", string(paramstest.Today()))
	page := h.Login.Get("/router-scripts/1/publish").Body.String()
	checkNoInline(t, "publish", page)
	for _, want := range []string{"Publish v1", "1 warning", "line 66", "the last values read are minVer, ver, v", `name="notes"`,
		`name="confirm"`, "Publish with these warnings", "v1 becomes the current version. New generations use it.", "The slug fresh-router is fixed from now on.",
		"The 0 generations made so far keep their versions.", `name="revision" value="1"`} {
		if !strings.Contains(page, want) {
			t.Errorf("publish page has no %q", want)
		}
	}
	// an unticked warning → 422
	res := h.Login.Post("/router-scripts/1/publish", url.Values{"revision": {"1"}, "notes": {"first"}})
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "Tick the box to publish with the warnings.") {
		t.Errorf("unticked: %d", res.Code)
	}
	// errors → 422, nothing written
	create(t, h, "broken", string(paramstest.Annotate(paramstest.WithEnd(paramstest.Today()), "subUrl", "@fil subscription-link")))
	page = h.Login.Get("/router-scripts/2/publish").Body.String()
	if !strings.Contains(page, "1 error") || !strings.Contains(page, "Unknown annotation @fil.") || strings.Contains(page, `name="confirm"`) {
		t.Errorf("errors page:\n%s", page)
	}
	res = h.Login.Post("/router-scripts/2/publish", url.Values{"revision": {"1"}, "confirm": {"1"}})
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("errors: %d", res.Code)
	}
	// a stale revision → 409
	res = h.Login.Post("/router-scripts/1/publish", url.Values{"revision": {"7"}, "confirm": {"1"}})
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "The draft changed since you opened it") {
		t.Errorf("stale: %d", res.Code)
	}
	if len(h.Events("routerscript.version_published")) != 0 {
		t.Fatal("published")
	}
	res = h.Login.Post("/router-scripts/1/publish", url.Values{"revision": {"1"}, "confirm": {"1"}, "notes": {"first"}})
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/router-scripts/1?published=1" {
		t.Fatalf("publish: %d %s", res.Code, res.Header().Get("Location"))
	}
	if page := h.Login.Get("/router-scripts/1?published=1").Body.String(); !strings.Contains(page, "Published v1.") || !strings.Contains(page, "first") {
		t.Error("no band after publishing")
	}
}

func TestVersionAndDiffPages(t *testing.T) {
	h := rscriptstest.New(t)
	today := paramstest.Today()
	h.Script("fresh-router", today)
	h.Publish(1, paramstest.Replace(paramstest.WithEnd(today), `"10.230.1"`, `"10.40.1"`))
	page := h.Login.Get("/router-scripts/1/versions/1").Body.String()
	checkNoInline(t, "version", page)
	for _, want := range []string{`class="c1"`, `class="na"`, `class="s"`, `class="nv"`, `id="body-L66"`, "the last values read are minVer, ver, v",
		`class="cl-note warning"`, "Detected parameters · 19", "/router-scripts/1/versions/1/download", "Make current", `name="from"`} {
		if !strings.Contains(page, want) {
			t.Errorf("version page has no %q", want)
		}
	}
	// the warning sits under line 66
	i66, inote := strings.Index(page, `id="body-L66"`), strings.Index(page, `cl-note warning`)
	if inote < i66 || inote > strings.Index(page, `id="body-L67"`) {
		t.Error("the warning is not under line 66")
	}
	// diff unified and split
	diff := h.Login.Get("/router-scripts/1/diff?from=1&to=2").Body.String()
	checkNoInline(t, "diff", diff)
	if !strings.Contains(diff, "10.40.1") || !strings.Contains(diff, "# END PARAMETERS") || !strings.Contains(diff, `class="diff"`) {
		t.Errorf("unified:\n%s", diff)
	}
	if split := h.Login.Get("/router-scripts/1/diff?from=1&to=2&view=split").Body.String(); !strings.Contains(split, `class="diff split"`) {
		t.Error("split")
	}
	if same := h.Login.Get("/router-scripts/1/diff?from=2&to=2").Body.String(); !strings.Contains(same, "The two are the same.") {
		t.Error("equal bodies")
	}
	// the default: the current version against the one before it
	if def := h.Login.Get("/router-scripts/1/diff").Body.String(); !strings.Contains(def, "fresh-router: v1 → v2") {
		t.Error("the default diff")
	}
	if _, err := h.Mod.Scripts.EditDraft(ctx, 1, "admin"); err != nil {
		t.Fatal(err)
	}
	if def := h.Login.Get("/router-scripts/1/diff").Body.String(); !strings.Contains(def, "fresh-router: v2 → draft") {
		t.Error("the default diff with a draft")
	}
	if h.Login.Get("/router-scripts/1/diff?from=9&to=2").Code != http.StatusNotFound {
		t.Error("a missing version")
	}
	// download: the exact bytes
	res := h.Login.Get("/router-scripts/1/versions/1/download")
	if res.Code != http.StatusOK || res.Body.String() != string(today) || res.Header().Get("Content-Disposition") != `attachment; filename="fresh-router-v1.rsc"` ||
		res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("download: %d %v", res.Code, res.Header())
	}
	if h.Login.Get("/router-scripts/1/versions/3/download").Code != http.StatusNotFound {
		t.Error("v3")
	}
	// make current
	res = h.Login.Post("/router-scripts/1/versions/1/current", nil)
	if res.Code != http.StatusSeeOther || len(h.Events("routerscript.current_changed")) != 1 {
		t.Errorf("make current: %d", res.Code)
	}
	if page := h.Login.Get("/router-scripts/1?current=1").Body.String(); !strings.Contains(page, "v1 is current: new generations use it.") {
		t.Error("no band")
	}
	_ = html.UnescapeString
}

func TestNewAndDeletePages(t *testing.T) {
	h := rscriptstest.New(t)
	page := h.Login.Get("/router-scripts/new").Body.String()
	checkNoInline(t, "new", page)
	for _, want := range []string{`name="name"`, `name="slug"`, `name="description"`, `name="body"`, `type="file"`, `enctype="multipart/form-data"`} {
		if !strings.Contains(page, want) {
			t.Errorf("new page has no %q", want)
		}
	}
	file := paramstest.Today()
	res := h.Login.PostMultipart("/router-scripts", url.Values{"name": {"fresh-router.rsc"}, "body": {"ignored"}},
		map[string]sitetest.File{"file": {Name: "fresh-router.rsc", Data: file}})
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/router-scripts/1?created=1" {
		t.Fatalf("upload: %d %s", res.Code, res.Body.String())
	}
	s, _ := h.Mod.Scripts.Get(ctx, 1)
	d, _ := h.Mod.Scripts.Draft(ctx, 1)
	if s.Slug != "fresh-router" || d.Body != string(file) {
		t.Errorf("uploaded: %+v", s)
	}
	// field errors come back with the form
	res = h.Login.PostMultipart("/router-scripts", url.Values{"name": {"fresh-router.rsc"}, "body": {""}}, nil)
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "Another script has this name.") || !strings.Contains(res.Body.String(), "The script is empty.") {
		t.Errorf("errors: %d", res.Code)
	}

	// delete asks for the slug
	page = h.Login.Get("/router-scripts/1/delete").Body.String()
	checkNoInline(t, "delete", page)
	if !strings.Contains(page, "Type fresh-router") || !strings.Contains(page, `name="slug"`) {
		t.Errorf("delete page:\n%s", page)
	}
	if res := h.Login.Post("/router-scripts/1/delete", url.Values{"slug": {"fresh"}}); res.Code != http.StatusUnprocessableEntity {
		t.Errorf("wrong slug: %d", res.Code)
	}
	// with a generation it refuses
	if _, err := h.Mod.Scripts.Publish(ctx, 1, 1, "", true, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Exec(`INSERT INTO rscripts_generations (script_id, version, router_name, file_name, created_at, created_by) VALUES (1, 1, 'Dacha', 'x', '2026-10-09T12:00:00.000Z', 'admin')`)
	page = h.Login.Get("/router-scripts/1/delete").Body.String()
	if !strings.Contains(page, "1 generation was made from fresh-router; archive it instead.") || !strings.Contains(page, `data-action="rs.archive_instead"`) {
		t.Errorf("with a generation:\n%s", page)
	}
	if res := h.Login.Post("/router-scripts/1/delete", url.Values{"slug": {"fresh-router"}}); res.Code != http.StatusConflict {
		t.Errorf("delete with a generation: %d", res.Code)
	}
	h.Exec(`DELETE FROM rscripts_generations`)
	if res := h.Login.Post("/router-scripts/1/delete", url.Values{"slug": {"fresh-router"}}); res.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", res.Code)
	}
	if h.Login.Get("/router-scripts/1").Code != http.StatusNotFound || len(h.Events("routerscript.deleted")) != 1 {
		t.Error("not deleted")
	}
	// details
	create(t, h, "x", "y")
	if res := h.Login.Post("/router-scripts/2/details", url.Values{"name": {"x2"}, "slug": {""}, "description": {"d"}}); res.Code != http.StatusSeeOther {
		t.Errorf("details: %d", res.Code)
	}
	if s, _ := h.Mod.Scripts.Get(ctx, 2); s.Name != "x2" || s.Slug != "x2" || s.Description != "d" {
		t.Errorf("details: %+v", s)
	}
}

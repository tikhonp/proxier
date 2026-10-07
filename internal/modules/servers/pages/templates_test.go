package pages_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"golang.org/x/net/html"
)

type tplEnv struct {
	T    *testing.T
	Site *sitetest.Site
	L    *sitetest.Login
	Svc  *templates.Service
	St   *store.Store
}

func newTplEnv(t *testing.T) *tplEnv {
	t.Helper()
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	mod, _ := s.App.Module("servers")
	m := mod.(*servers.Module)
	return &tplEnv{T: t, Site: s, L: s.SignIn(""), Svc: m.Templates, St: m.Store}
}

func (e *tplEnv) create(slug string) int64 {
	e.T.Helper()
	id, err := e.Svc.Create(context.Background(), "Stack "+slug, slug, "about "+slug, "admin")
	if err != nil {
		e.T.Fatal(err)
	}
	return id
}

// form is what the editor posts: every file as file[<path>], the revision.
func form(rev int, files map[string]string) url.Values {
	v := url.Values{"revision": {fmt.Sprint(rev)}, "active": {"manifest.yaml"}}
	for p, c := range files {
		v.Set("file["+p+"]", c)
	}
	return v
}

// draftForm is the saved draft as a form, with changes applied.
func (e *tplEnv) draftForm(id int64, change map[string]string) url.Values {
	e.T.Helper()
	d, err := e.Svc.Draft(context.Background(), id)
	if err != nil {
		e.T.Fatal(err)
	}
	files := map[string]string{}
	for p, c := range d.Files {
		files[p] = string(c)
	}
	for p, c := range change {
		files[p] = c
	}
	return form(d.Revision, files)
}

func (e *tplEnv) hx(path string, f url.Values) *httptest.ResponseRecorder {
	e.T.Helper()
	f.Set("_csrf", e.L.CSRF)
	return e.Site.Do(sitetest.Req{Method: http.MethodPost, Path: path, Form: f, Cookies: []*http.Cookie{e.L.Cookie},
		Header: http.Header{"Hx-Request": {"true"}}})
}

func (e *tplEnv) publish(id int64, change map[string]string, notes string) int {
	e.T.Helper()
	ctx := context.Background()
	d, _ := e.Svc.EditDraft(ctx, id, "admin")
	files := map[string][]byte{}
	for p, c := range d.Files {
		files[p] = c
	}
	for p, c := range change {
		files[p] = []byte(c)
	}
	rev, err := e.Svc.SaveDraft(ctx, id, d.Revision, files, "admin", "admin")
	if err != nil {
		e.T.Fatal(err)
	}
	n, err := e.Svc.Publish(ctx, id, rev, notes, true, "admin")
	if err != nil {
		e.T.Fatal(err)
	}
	return n
}

func contains(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, shorten(body))
		}
	}
}

func lacks(t *testing.T, what, body string, nots ...string) {
	t.Helper()
	for _, w := range nots {
		if strings.Contains(body, w) {
			t.Errorf("%s has %q:\n%s", what, w, shorten(body))
		}
	}
}

func shorten(s string) string {
	if len(s) > 6000 {
		return s[:6000] + "…"
	}
	return s
}

func idIn(t *testing.T, loc string) int64 {
	t.Helper()
	m := regexp.MustCompile(`^/templates/(\d+)`).FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("no template id in %q", loc)
	}
	var id int64
	_, _ = fmt.Sscan(m[1], &id)
	return id
}

func TestTemplatesListAndNewPages(t *testing.T) {
	e := newTplEnv(t)
	if rec := e.Site.Do(sitetest.Req{Path: "/templates"}); rec.Code == http.StatusOK {
		t.Fatal("the list is open to a signed-out visitor")
	}
	// the seed template is there from the first start
	body := e.L.Get("/templates").Body.String()
	contains(t, "list with the seed", body, "VLESS XHTTP behind nginx", "vless-xhttp", "New template", "Import…", "none yet")
	seed, _ := e.Svc.List(context.Background(), false)
	if err := e.Svc.Delete(context.Background(), seed[0].ID, "admin"); err != nil {
		t.Fatal(err)
	}
	contains(t, "empty list", e.L.Get("/templates").Body.String(), "No templates yet.")

	// a bad new template: errors inline, what was typed kept
	rec := e.L.Post("/templates", url.Values{"name": {""}, "slug": {"X!"}, "description": {"hello"}})
	body = rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad new template: %d", rec.Code)
	}
	contains(t, "bad new", body, "Use 1–80 characters.", "Use a lower-case letter", `value="X!"`, `value="hello"`)

	// a good one: the slug comes from the name, and the editor opens
	rec = e.L.Post("/templates", url.Values{"name": {"My Fresh Stack"}, "slug": {""}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("new template: %d %s", rec.Code, rec.Body)
	}
	id := idIn(t, rec.Header().Get("Location"))
	if !strings.HasSuffix(rec.Header().Get("Location"), "/edit") {
		t.Errorf("location %q", rec.Header().Get("Location"))
	}
	info, _ := e.Svc.Get(context.Background(), id)
	if info.Slug != "my-fresh-stack" {
		t.Errorf("slug %q", info.Slug)
	}
	// the same slug again
	rec = e.L.Post("/templates", url.Values{"name": {"Other"}, "slug": {"my-fresh-stack"}})
	contains(t, "taken slug", rec.Body.String(), "This slug is already used.")

	// the list shows it with its draft, and the archived filter hides and shows
	body = e.L.Get("/templates").Body.String()
	contains(t, "list", body, "My Fresh Stack", "my-fresh-stack", "draft", "new, never published", "Archived · 0")
	if rec := e.L.Post(fmt.Sprintf("/templates/%d/archive", id), nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("archive: %d", rec.Code)
	}
	lacks(t, "active list", e.L.Get("/templates").Body.String(), "my-fresh-stack")
	contains(t, "archived list", e.L.Get("/templates?archived=1").Body.String(), "my-fresh-stack", "Archived · 1")
	e.L.Post(fmt.Sprintf("/templates/%d/unarchive", id), nil)
	contains(t, "after unarchive", e.L.Get("/templates").Body.String(), "my-fresh-stack")
}

func TestEditorSaveAndValidate(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	rec := e.L.Post("/templates", url.Values{"name": {"Edit me"}, "slug": {"edit-me"}})
	id := idIn(t, rec.Header().Get("Location"))

	page := e.L.Get(fmt.Sprintf("/templates/%d/edit", id))
	body := page.Body.String()
	contains(t, "editor", body, `name="file[manifest.yaml]"`, `name="file[compose.yaml]"`, `name="revision"`, `value="1"`,
		"services:", "Save draft", "Validate", "Preview", "Publish v1", `src="/static/`, "editor.bundle.js", `type="module"`)

	// Save: the whole draft is stored as one revision; the browser's CRLF becomes LF
	d, _ := e.Svc.Draft(ctx, id)
	compose := strings.Replace(string(d.Files["compose.yaml"]), "nginx:stable-alpine", "nginx:latest", 1)
	f := form(1, map[string]string{"manifest.yaml": string(d.Files["manifest.yaml"]), "compose.yaml": strings.ReplaceAll(compose, "\n", "\r\n")})
	f.Set("active", "compose.yaml")
	rec = e.L.Post(fmt.Sprintf("/templates/%d/draft", id), f)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "saved=1") || !strings.Contains(rec.Header().Get("Location"), "file=compose.yaml") {
		t.Fatalf("save: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	d, _ = e.Svc.Draft(ctx, id)
	if d.Revision != 2 || string(d.Files["compose.yaml"]) != compose || bytes.Contains(d.Files["compose.yaml"], []byte("\r")) {
		t.Errorf("revision %d, compose %q", d.Revision, d.Files["compose.yaml"])
	}
	contains(t, "after save", e.L.Get(fmt.Sprintf("/templates/%d/edit?saved=1", id)).Body.String(), "Draft saved.", `name="revision" id="ed-revision" value="2"`)

	// Validate: saves what changed, then lists findings per file with the line a link selects
	manifest := string(d.Files["manifest.yaml"])
	manifest = strings.Replace(manifest, "  - { path: compose.yaml, validate: compose }", "  - { path: compose.yaml, validate: compose }\n  - { path: bad.json, validate: json }", 1)
	vf := form(2, map[string]string{"manifest.yaml": manifest, "compose.yaml": compose, "bad.json": "{\n  \"a\": ,\n}\n"})
	rec = e.hx(fmt.Sprintf("/templates/%d/draft/validate", id), vf)
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("validate: %d %s", rec.Code, body)
	}
	contains(t, "report", body, "1 error", "1 warning", `data-path="bad.json"`, `data-line="2"`, `data-sev="error"`, "bad.json:2",
		`data-path="compose.yaml"`, `data-sev="warning"`, "nginx:latest", `id="ed-revision"`, `value="3"`, "hx-swap-oob")
	d, _ = e.Svc.Draft(ctx, id)
	if d.Revision != 3 || string(d.Files["bad.json"]) == "" {
		t.Errorf("validate did not save first: revision %d", d.Revision)
	}
	// a clean draft says so
	clean := e.draftForm(id, nil)
	clean.Set("file[manifest.yaml]", string(templates.Skeleton("Edit me", "edit-me", "")["manifest.yaml"]))
	clean.Set("file[compose.yaml]", string(templates.Skeleton("Edit me", "edit-me", "")["compose.yaml"]))
	delete(clean, "file[bad.json]")
	rec = e.hx(fmt.Sprintf("/templates/%d/draft/validate", id), clean)
	contains(t, "clean report", rec.Body.String(), "No problems found")

	// bad paths are refused without touching the draft
	rec = e.L.Post(fmt.Sprintf("/templates/%d/draft", id), form(4, map[string]string{"manifest.yaml": "x", "../evil": "x"}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad path: %d", rec.Code)
	}
	contains(t, "bad path", rec.Body.String(), "must be relative")
}

func TestEditorStaleSave(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	id := e.create("stale")
	d, _ := e.Svc.Draft(ctx, id)
	files := map[string]string{"manifest.yaml": string(d.Files["manifest.yaml"]), "compose.yaml": string(d.Files["compose.yaml"])}

	// two tabs open the draft at revision 1; the first saves
	tabA := map[string]string{"manifest.yaml": files["manifest.yaml"], "compose.yaml": files["compose.yaml"] + "# from tab A\n"}
	if rec := e.L.Post(fmt.Sprintf("/templates/%d/draft", id), form(1, tabA)); rec.Code != http.StatusSeeOther {
		t.Fatalf("tab A: %d", rec.Code)
	}
	// the second tab saves what it has: refused, nothing overwritten, its text stays in the page
	tabB := map[string]string{"manifest.yaml": files["manifest.yaml"], "compose.yaml": files["compose.yaml"] + "# from tab B\n"}
	rec := e.L.Post(fmt.Sprintf("/templates/%d/draft", id), form(1, tabB))
	body := rec.Body.String()
	if rec.Code != http.StatusConflict {
		t.Fatalf("tab B: %d", rec.Code)
	}
	contains(t, "stale page", body, "The draft changed since you opened it", "Copy my version", "Reload", "from tab B", "data-copy-draft")
	lacks(t, "stale page", body, "from tab A")
	got, _ := e.Svc.Draft(ctx, id)
	if !strings.Contains(string(got.Files["compose.yaml"]), "from tab A") || strings.Contains(string(got.Files["compose.yaml"]), "from tab B") || got.Revision != 2 {
		t.Errorf("the draft was overwritten: revision %d %q", got.Revision, got.Files["compose.yaml"])
	}
	// Save and Publish are off in that page
	if !regexp.MustCompile(`data-action="tpl.save" disabled`).MatchString(body) {
		t.Error("Save is not disabled in the stale editor")
	}

	// validating with the old revision gets the band in the side panel, and saves nothing
	rec = e.hx(fmt.Sprintf("/templates/%d/draft/validate", id), form(1, tabB))
	contains(t, "stale validate", rec.Body.String(), "The draft changed since you opened it", "data-copy-draft")
	if got, _ = e.Svc.Draft(ctx, id); got.Revision != 2 {
		t.Errorf("validate saved over a stale revision: %d", got.Revision)
	}
	// the publish screen of a stale revision is refused too
	rec = e.L.Post(fmt.Sprintf("/templates/%d/publish", id), url.Values{"revision": {"1"}, "notes": {"x"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("stale publish: %d", rec.Code)
	}
	if n, _ := e.Svc.Versions(ctx, id); len(n) != 0 {
		t.Error("a stale publish created a version")
	}
}

func TestPreviewUsesPostedFiles(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	id := e.create("preview")
	d, _ := e.Svc.Draft(ctx, id)
	compose := string(d.Files["compose.yaml"]) + "# server {{ .Server.Name }} at {{ .Server.IP }}\n"
	rec := e.hx(fmt.Sprintf("/templates/%d/draft/preview", id), form(1, map[string]string{"manifest.yaml": string(d.Files["manifest.yaml"]), "compose.yaml": compose}))
	body := rec.Body.String()
	contains(t, "preview", body, "# server xx-1 at 192.0.2.10", "compose.yaml", "sample server xx-1")
	lacks(t, "preview", body, "{{")
	if got, _ := e.Svc.Draft(ctx, id); got.Revision != 1 || strings.Contains(string(got.Files["compose.yaml"]), "Server.Name") {
		t.Errorf("preview stored something: revision %d", got.Revision)
	}
	// a render error is a finding, and the other files still show
	bad := string(d.Files["compose.yaml"]) + "# {{ .Gen.nope }}\n"
	rec = e.hx(fmt.Sprintf("/templates/%d/draft/preview", id), form(1, map[string]string{"manifest.yaml": string(d.Files["manifest.yaml"]), "compose.yaml": bad}))
	contains(t, "preview with an error", rec.Body.String(), "compose.yaml", "nope")
	// a real server is not available yet
	f := form(1, map[string]string{"manifest.yaml": string(d.Files["manifest.yaml"])})
	f.Set("server", "7")
	contains(t, "preview for a server", e.hx(fmt.Sprintf("/templates/%d/draft/preview", id), f).Body.String(), "arrives with servers")
}

func TestPublishFlow(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	id := e.create("flow")
	d, _ := e.Svc.Draft(ctx, id)
	pub := fmt.Sprintf("/templates/%d/publish", id)

	// errors: the report shows, the button is off, nothing is created
	if _, err := e.Svc.SaveDraft(ctx, id, 1, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"]}, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	body := e.L.Get(pub).Body.String()
	contains(t, "publish with errors", body, "Publish v1", "Fix them in the editor", "compose.yaml", "disabled")
	rec := e.L.Post(pub, url.Values{"revision": {"2"}, "notes": {"first"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish with errors: %d", rec.Code)
	}
	if v, _ := e.Svc.Versions(ctx, id); len(v) != 0 {
		t.Fatal("a version was created with errors")
	}

	// warnings: the screen says so, an unconfirmed post is refused, a confirmed one publishes
	compose := strings.Replace(string(d.Files["compose.yaml"]), "nginx:stable-alpine", "nginx:latest", 1)
	if _, err := e.Svc.SaveDraft(ctx, id, 2, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"], "compose.yaml": []byte(compose)}, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	body = e.L.Get(pub).Body.String()
	contains(t, "publish with warnings", body, "Validation passed with 1 warnings", "Publish v1 with 1 warnings", `name="confirm_warnings" value="1"`, "nginx:latest", "Version notes")
	rec = e.L.Post(pub, url.Values{"revision": {"3"}, "notes": {"first"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Publishing confirms them.") {
		t.Fatalf("unconfirmed warnings: %d", rec.Code)
	}
	rec = e.L.Post(pub, url.Values{"revision": {"3"}, "notes": {"first release"}, "confirm_warnings": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/templates/%d?saved=published&v=1", id) {
		t.Fatalf("publish: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	body = e.L.Get(rec.Header().Get("Location")).Body.String()
	contains(t, "template page", body, "Published version 1.", "first release", "default v1", "1 warning")

	// edit again: a new draft based on v1; publishing it makes v2
	if rec := e.L.Get(fmt.Sprintf("/templates/%d/edit", id)); rec.Code != http.StatusOK {
		t.Fatalf("edit: %d", rec.Code)
	}
	nd, _ := e.Svc.Draft(ctx, id)
	if nd.BasedOn != 1 {
		t.Errorf("based on %d", nd.BasedOn)
	}
	clean := strings.Replace(compose, "nginx:latest", "nginx:1.27-alpine", 1)
	if _, err := e.Svc.SaveDraft(ctx, id, nd.Revision, map[string][]byte{"manifest.yaml": nd.Files["manifest.yaml"], "compose.yaml": []byte(clean)}, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	body = e.L.Get(pub).Body.String()
	contains(t, "publish v2", body, "Validation passed.", "Publish v2", "New servers keep using v1 until you make v2 the default")
	rec = e.L.Post(pub, url.Values{"revision": {fmt.Sprint(nd.Revision + 1)}, "notes": {"pinned"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("publish v2: %d %s", rec.Code, rec.Body)
	}
	body = e.L.Get(fmt.Sprintf("/templates/%d", id)).Body.String()
	contains(t, "after v2", body, ">v2<", ">v1<", "pinned", "Make default", "Start a draft")

	// the editor's own Publish button saves first and goes to this screen
	if _, err := e.Svc.EditDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	nd, _ = e.Svc.Draft(ctx, id)
	f := e.draftForm(id, map[string]string{"compose.yaml": clean + "# more\n"})
	f.Set("next", "publish")
	rec = e.L.Post(fmt.Sprintf("/templates/%d/draft", id), f)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != pub {
		t.Errorf("save and publish: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got, _ := e.Svc.Draft(ctx, id); got.Revision != nd.Revision+1 {
		t.Errorf("not saved first: %d", got.Revision)
	}
}

func TestVersionPagesAndActions(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	id := e.create("vers")
	e.publish(id, nil, "one")
	e.publish(id, map[string]string{"compose.yaml": "services:\n  web:\n    image: nginx:1.27\n"}, "two")

	body := e.L.Get(fmt.Sprintf("/templates/%d/versions/1", id)).Body.String()
	contains(t, "version view", body, "Stack vers v1", "manifest.yaml", "compose.yaml", `class="cl`, `id="code-L1"`, "read-only", "Export")
	lacks(t, "version view of the default", body, "Make default")
	body = e.L.Get(fmt.Sprintf("/templates/%d/versions/2?file=compose.yaml", id)).Body.String()
	contains(t, "version 2 file", body, "nginx:1.27", "Diff", "Make default", `href="/templates/`+fmt.Sprint(id)+`/diff?a=1&amp;b=2"`)
	if rec := e.L.Get(fmt.Sprintf("/templates/%d/versions/9", id)); rec.Code != http.StatusNotFound {
		t.Errorf("a missing version: %d", rec.Code)
	}

	// make default: strength 1 button; the change shows
	rec := e.L.Post(fmt.Sprintf("/templates/%d/versions/2/default", id), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("make default: %d", rec.Code)
	}
	contains(t, "after default", e.L.Get(rec.Header().Get("Location")).Body.String(), "Version 2 is now the default")
	if info, _ := e.Svc.Get(ctx, id); info.DefaultVersion != 2 {
		t.Errorf("default %d", info.DefaultVersion)
	}

	// a new draft from v1: the page offers it, a draft that exists needs the consequences dialog
	if err := e.Svc.DiscardDraft(ctx, id, "admin"); err != nil && !errors.Is(err, templates.ErrNoDraft) {
		t.Fatal(err)
	}
	if rec := e.L.Post(fmt.Sprintf("/templates/%d/versions/1/draft", id), nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("draft from v1: %d", rec.Code)
	}
	d, _ := e.Svc.Draft(ctx, id)
	if d.BasedOn != 1 || d.Source.Kind != "version" {
		t.Errorf("draft %+v", d)
	}
	body = e.L.Get(fmt.Sprintf("/templates/%d", id)).Body.String()
	contains(t, "template page with a draft", body, "Replace the draft with v1?", "Publishing the new draft makes v3.", "based on v1", "Discard draft…", "Discard the draft?")

	// discard
	rec = e.L.Post(fmt.Sprintf("/templates/%d/draft/discard", id), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("discard: %d", rec.Code)
	}
	if _, err := e.Svc.Draft(ctx, id); err == nil {
		t.Error("the draft survived")
	}
	contains(t, "after discard", e.L.Get(rec.Header().Get("Location")).Body.String(), "Draft discarded.")

	// rename: slug read-only after a version
	body = e.L.Get(fmt.Sprintf("/templates/%d", id)).Body.String()
	contains(t, "details", body, `name="slug" value="vers" autocomplete="off" spellcheck="false" readonly`)
	rec = e.L.Post(fmt.Sprintf("/templates/%d", id), url.Values{"name": {"Renamed"}, "slug": {"other"}, "description": {"d"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: %d", rec.Code)
	}
	if info, _ := e.Svc.Get(ctx, id); info.Name != "Renamed" || info.Slug != "vers" {
		t.Errorf("after rename: %+v", info)
	}
}

func TestDiffPage(t *testing.T) {
	e := newTplEnv(t)
	id := e.create("diffs")
	e.publish(id, nil, "one")
	d, _ := e.Svc.EditDraft(context.Background(), id, "admin")
	withExtra := strings.Replace(string(d.Files["manifest.yaml"]), "  - { path: compose.yaml, validate: compose }", "  - { path: compose.yaml, validate: compose }\n  - { path: extra.txt }", 1)
	e.publish(id, map[string]string{"manifest.yaml": withExtra, "compose.yaml": "services:\n  web:\n    image: nginx:1.27\n", "extra.txt": "hello\n"}, "two")

	body := e.L.Get(fmt.Sprintf("/templates/%d/diff?a=1&b=2", id)).Body.String()
	contains(t, "unified diff", body, `class="diff"`, `class="dl add"`, `class="dl del"`, "@@", "nginx:1.27", "extra.txt", "added", "changed", "−", "3 files differ", `aria-pressed="true">Unified`)
	lacks(t, "unified diff", body, `class="drow"`)

	body = e.L.Get(fmt.Sprintf("/templates/%d/diff?a=1&b=2&view=split", id)).Body.String()
	contains(t, "split diff", body, `class="diff split"`, `class="drow"`, `dl half add`, `dl half del`, `aria-pressed="true">Side by side`)

	// defaults: the newest against the one before
	contains(t, "default diff", e.L.Get(fmt.Sprintf("/templates/%d/diff", id)).Body.String(), "nginx:1.27")
	// the same version twice, and one version only
	contains(t, "same", e.L.Get(fmt.Sprintf("/templates/%d/diff?a=2&b=2", id)).Body.String(), "Choose two different versions.")
	id2 := e.create("single")
	e.publish(id2, nil, "only")
	contains(t, "single version", e.L.Get(fmt.Sprintf("/templates/%d/diff", id2)).Body.String(), "A diff needs two versions.")
}

func TestExportDownload(t *testing.T) {
	e := newTplEnv(t)
	id := e.create("export-me")
	e.publish(id, nil, "one")
	rec := e.L.Get(fmt.Sprintf("/templates/%d/versions/1/export", id))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("export: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="export-me-v1.zip"` {
		t.Errorf("disposition %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "compose.yaml,manifest.yaml" {
		t.Errorf("entries %v", names)
	}
	if rec := e.L.Get(fmt.Sprintf("/templates/%d/versions/4/export", id)); rec.Code != http.StatusNotFound {
		t.Errorf("a version that does not exist: %d", rec.Code)
	}
}

func zipOf(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range entries {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(c))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func TestImportZipPages(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	skel := templates.Skeleton("Zipped stack", "zipped", "from a zip")
	archive := zipOf(t, map[string]string{"zipped/manifest.yaml": string(skel["manifest.yaml"]), "zipped/compose.yaml": string(skel["compose.yaml"])})

	body := e.L.Get("/templates/import").Body.String()
	contains(t, "import form", body, `type="file"`, `name="zip"`, `name="url"`, `name="ref"`, `name="path"`, "A new template")

	// into a new template: the name and slug come from the manifest; the editor opens
	rec := e.L.PostMultipart("/templates/import", url.Values{"source": {"zip"}, "into": {"new"}}, map[string]sitetest.File{"zip": {Name: "stack.zip", Data: archive}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	id := idIn(t, rec.Header().Get("Location"))
	info, _ := e.Svc.Get(ctx, id)
	if info.Name != "Zipped stack" || info.Slug != "zipped-stack" {
		t.Errorf("template %+v", info)
	}
	d, _ := e.Svc.Draft(ctx, id)
	if d.Source.Kind != "zip" || d.Source.Name != "stack.zip" || len(d.Files) != 2 {
		t.Errorf("draft %+v (%d files)", d.Source, len(d.Files))
	}
	contains(t, "editor after import", e.L.Get(rec.Header().Get("Location")).Body.String(), "imported from stack.zip")

	// into one that has a draft: confirmation first, nothing replaced
	other := e.create("has-draft")
	before, _ := e.Svc.Draft(ctx, other)
	rec = e.L.PostMultipart("/templates/import", url.Values{"source": {"zip"}, "into": {fmt.Sprint(other)}}, map[string]sitetest.File{"zip": {Name: "stack.zip", Data: archive}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("without confirmation: %d", rec.Code)
	}
	contains(t, "confirm needed", rec.Body.String(), "has a draft, and importing replaces it", "has a draft")
	if after, _ := e.Svc.Draft(ctx, other); after.Revision != before.Revision {
		t.Error("the draft was replaced without confirmation")
	}
	rec = e.L.PostMultipart("/templates/import", url.Values{"source": {"zip"}, "into": {fmt.Sprint(other)}, "confirm": {"1"}}, map[string]sitetest.File{"zip": {Name: "stack.zip", Data: archive}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed: %d %s", rec.Code, rec.Body)
	}
	if after, _ := e.Svc.Draft(ctx, other); after.Revision != before.Revision+1 || after.Source.Kind != "zip" {
		t.Errorf("after confirmed import: %+v", after)
	}

	// refusals show inline
	deep := zipOf(t, map[string]string{"a/b/manifest.yaml": "x"})
	rec = e.L.PostMultipart("/templates/import", url.Values{"source": {"zip"}, "into": {"new"}}, map[string]sitetest.File{"zip": {Name: "deep.zip", Data: deep}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("deep zip: %d", rec.Code)
	}
	contains(t, "deep zip", rec.Body.String(), "manifest.yaml not found at the root")
	rec = e.L.PostMultipart("/templates/import", url.Values{"source": {"zip"}, "into": {"new"}}, nil)
	contains(t, "no file", rec.Body.String(), "Choose a file.")
	rec = e.L.PostMultipart("/templates/import", url.Values{"source": {"git"}, "into": {"new"}, "url": {"http://example.com/r.git"}}, nil)
	contains(t, "git url", rec.Body.String(), "Use an https:// repository URL")
}

func TestDraftUploadKeepsEdits(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	id := e.create("upload")
	d, _ := e.Svc.Draft(ctx, id)
	f := form(1, map[string]string{"manifest.yaml": string(d.Files["manifest.yaml"]), "compose.yaml": "services: {}\n# unsaved edit\n"})
	f.Set("dir", "site")
	rec := e.L.PostMultipart(fmt.Sprintf("/templates/%d/draft/upload", id), f, map[string]sitetest.File{"upload": {Name: "index.html", Data: []byte("<h1>hi</h1>\n")}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, body)
	}
	contains(t, "upload response", body, `id="ed-files"`, `name="file[site/index.html]"`, "&lt;h1&gt;hi&lt;/h1&gt;", "# unsaved edit", `data-dir="site"`, `data-active="site/index.html"`)
	if got, _ := e.Svc.Draft(ctx, id); got.Revision != 1 || len(got.Files) != 2 {
		t.Errorf("an upload saved something: revision %d, %d files", got.Revision, len(got.Files))
	}
	// a zip unpacks into the tree
	skel := templates.Skeleton("Z", "zz", "")
	z := zipOf(t, map[string]string{"manifest.yaml": string(skel["manifest.yaml"]), "nginx/site.conf": "server {}\n"})
	rec = e.L.PostMultipart(fmt.Sprintf("/templates/%d/draft/upload", id), form(1, map[string]string{"manifest.yaml": "x"}), map[string]sitetest.File{"upload": {Name: "more.zip", Data: z}})
	contains(t, "zip upload", rec.Body.String(), `name="file[nginx/site.conf]"`, `name="file[manifest.yaml]"`)
	// a refused zip leaves the tree and says why in #ed-msg
	rec = e.L.PostMultipart(fmt.Sprintf("/templates/%d/draft/upload", id), form(1, map[string]string{"manifest.yaml": "x"}), map[string]sitetest.File{"upload": {Name: "bad.zip", Data: []byte("nope")}})
	contains(t, "bad zip", rec.Body.String(), `name="file[manifest.yaml]"`, "This is not a zip file.", `id="ed-msg"`)
	// binary files travel as base64 and survive a save
	rec = e.L.PostMultipart(fmt.Sprintf("/templates/%d/draft/upload", id), form(1, map[string]string{"manifest.yaml": "x"}), map[string]sitetest.File{"upload": {Name: "logo.png", Data: []byte{0x89, 'P', 'N', 'G', 0, 1, 2}}})
	contains(t, "binary upload", rec.Body.String(), `name="bin[logo.png]"`, `value="iVBORwABAg=="`)
	save := form(1, map[string]string{"manifest.yaml": string(d.Files["manifest.yaml"]), "compose.yaml": string(d.Files["compose.yaml"])})
	save.Set("bin[logo.png]", "iVBORwABAg==")
	if rec := e.L.Post(fmt.Sprintf("/templates/%d/draft", id), save); rec.Code != http.StatusSeeOther {
		t.Fatalf("save with a binary: %d", rec.Code)
	}
	if got, _ := e.Svc.Draft(ctx, id); !bytes.Equal(got.Files["logo.png"], []byte{0x89, 'P', 'N', 'G', 0, 1, 2}) {
		t.Errorf("the binary file: %v", got.Files["logo.png"])
	}
}

func TestDeleteUsedTemplatePage(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	id := e.create("in-use")
	e.publish(id, nil, "one")
	loc, err := e.St.CreateLocation(ctx, "nl", "Netherlands", "NL", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Site.App.DB.W.ExecContext(ctx, `INSERT INTO servers_servers (location_id, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at, retired_at)
		VALUES (?, 1, 'nl-1', '203.0.113.7', 'h', 'h', 'retired', ?, 1, '2026-10-07T00:00:00.000Z', '2026-10-07T00:00:00.000Z')`, loc, id); err != nil {
		t.Fatal(err)
	}
	page := e.L.Get(fmt.Sprintf("/templates/%d", id)).Body.String()
	contains(t, "delete dialog", page, `data-confirm-name="in-use"`, "Delete template Stack in-use?", "1 server built from it")

	rec := e.L.Post(fmt.Sprintf("/templates/%d/delete", id), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete: %d", rec.Code)
	}
	body := rec.Body.String()
	contains(t, "refused delete", body, "Archive it instead", fmt.Sprintf(`action="/templates/%d/archive"`, id), "retired ones count")
	if _, err := e.Svc.Get(ctx, id); err != nil {
		t.Errorf("the template is gone: %v", err)
	}
	// an unused one is deleted
	free := e.create("unused")
	rec = e.L.Post(fmt.Sprintf("/templates/%d/delete", free), nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/templates?saved=deleted" {
		t.Errorf("delete unused: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := e.Svc.Get(ctx, free); err == nil {
		t.Error("the unused template is still there")
	}
}

// checkNoInline fails on anything the CSP would block: <style>, inline
// scripts, style and on* attributes, javascript: URLs.
func checkNoInline(t *testing.T, name, page string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "style" {
				t.Errorf("%s: <style> element", name)
			}
			if n.Data == "script" {
				src := false
				for _, a := range n.Attr {
					if a.Key == "src" {
						src = true
					}
				}
				if !src {
					t.Errorf("%s: inline <script>", name)
				}
			}
			for _, a := range n.Attr {
				if a.Key == "style" || strings.HasPrefix(a.Key, "on") || strings.HasPrefix(a.Key, "hx-on") ||
					((a.Key == "href" || a.Key == "src") && strings.HasPrefix(strings.ToLower(a.Val), "javascript:")) {
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

func TestTemplatePagesHaveNoInlineScriptOrStyle(t *testing.T) {
	e := newTplEnv(t)
	ctx := context.Background()
	id := e.create("csp")
	e.publish(id, nil, "one")
	e.publish(id, map[string]string{"compose.yaml": "services:\n  web:\n    image: nginx:1.27\n"}, "two")
	if _, err := e.Svc.EditDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	other := e.create("bad")
	d, _ := e.Svc.Draft(ctx, other)
	if _, err := e.Svc.SaveDraft(ctx, other, d.Revision, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"]}, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	pub := func(n int64) string { return fmt.Sprintf("/templates/%d/publish", n) }
	pages := map[string]string{
		"list":          e.L.Get("/templates").Body.String(),
		"archived":      e.L.Get("/templates?archived=1").Body.String(),
		"new":           e.L.Get("/templates/new").Body.String(),
		"template":      e.L.Get(fmt.Sprintf("/templates/%d", id)).Body.String(),
		"version":       e.L.Get(fmt.Sprintf("/templates/%d/versions/2?file=compose.yaml", id)).Body.String(),
		"diff":          e.L.Get(fmt.Sprintf("/templates/%d/diff?a=1&b=2", id)).Body.String(),
		"diff split":    e.L.Get(fmt.Sprintf("/templates/%d/diff?a=1&b=2&view=split", id)).Body.String(),
		"editor":        e.L.Get(fmt.Sprintf("/templates/%d/edit", id)).Body.String(),
		"publish":       e.L.Get(pub(id)).Body.String(),
		"publish error": e.L.Get(pub(other)).Body.String(),
		"import":        e.L.Get("/templates/import").Body.String(),
		"validate":      e.hx(fmt.Sprintf("/templates/%d/draft/validate", other), e.draftForm(other, nil)).Body.String(),
		"preview":       e.hx(fmt.Sprintf("/templates/%d/draft/preview", id), e.draftForm(id, nil)).Body.String(),
		"stale":         e.L.Post(fmt.Sprintf("/templates/%d/draft", id), form(99, map[string]string{"manifest.yaml": "x"})).Body.String(),
	}
	for name, body := range pages {
		if len(body) < 50 {
			t.Errorf("%s: empty page", name)
		}
		checkNoInline(t, name, body)
	}
}

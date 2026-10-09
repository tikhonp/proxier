package pages_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

const srBase = "[General]\ndns-server = system\n\n[Rule]\nRULE-SET,https://example.com/ads.list,REJECT\nFINAL,DIRECT\n"

func srSetup(t *testing.T) (*routingtest.Harness, int64, string) {
	t.Helper()
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	h.List("Main", h.Upstream("anthropic"))
	id, err := h.Mod.Shadowrocket.Create(bg(), shadowrocket.New{Name: "iphone", ListID: 1, Policy: "PROXY", Base: srBase}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := h.Mod.Shadowrocket.URL(bg(), id)
	return h, id, strings.Split(u, "/")[4]
}

func TestShadowrocketPages(t *testing.T) {
	h, _, token := srSetup(t)
	// a fetch, for the log and the line
	h.Site.Do(sitetest.Req{Path: "/r/" + token + "/iphone.conf", Addr: "198.51.100.23:5000", Header: http.Header{"User-Agent": {"Shadowrocket/2.2.62 CFNetwork/1568"}}})

	list := h.Login.Get("/routing/shadowrocket").Body.String()
	has(t, "configs", list, "A hosted config per phone", `href="/routing/shadowrocket/new"`, `data-key="n"`, ">iphone<",
		"base v1 · 2 rules", ">Main<", ">PROXY<", "…/r/••••••/iphone.conf", "Shadowrocket/2.2.62 CFNetwork/1568")
	if strings.Contains(list, token) {
		t.Error("the configs list holds the token")
	}
	checkNoInline(t, "configs", list)

	rec := h.Login.Get("/routing/shadowrocket/1")
	page := rec.Body.String()
	has(t, "config page", page, "<h1>iphone</h1>", "routing list Main · policy PROXY · base config v1 · fetched", `data-key="e"`,
		"http://proxier.test/r/••••••••••••••••/iphone.conf", `hx-get="/routing/shadowrocket/1/url"`, `hx-get="/routing/shadowrocket/1/qr"`,
		"In Shadowrocket: Config → + → paste the URL → Use config", `data-key="y"`,
		"What the phone gets", `class="sr-line sr-ours"`, "DOMAIN-SUFFIX,anthropic.com,PROXY", "RULE-SET,https://example.com/ads.list,REJECT",
		`href="/routing/shadowrocket/1/output"`, "Download iphone.conf",
		"Base config versions", ">v1<", "current", `href="/routing/shadowrocket/1/versions/1"`,
		"Fetches", "kept 90 days", "198.51.100.23",
		"Actions", "Change routing list or policy…", "Regenerate URL…", "The old URL stops working at once. Paste the new one into Shadowrocket.",
		"Disable…", "The URL answers 404 until you enable it; Shadowrocket keeps its last config.", "Delete…",
		"Delete iphone? Its URL answers 404; its versions and fetch log go too.",
		"Activity", "Created Shadowrocket config iphone")
	checkNoInline(t, "config page", page)
	if !strings.Contains(page, `class="sr-line"`) {
		t.Error("no base lines in the preview")
	}

	// Reveal and QR: fragments with no-store
	rec = h.Login.Get("/routing/shadowrocket/1/url", http.Header{"Hx-Request": {"true"}})
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "http://proxier.test/r/"+token+"/iphone.conf") {
		t.Errorf("reveal: %d %v", rec.Code, rec.Header())
	}
	if rec := h.Login.Get("/routing/shadowrocket/1/url?hide=1"); !strings.Contains(rec.Body.String(), "••••") {
		t.Error("hide")
	}
	rec = h.Login.Get("/routing/shadowrocket/1/qr")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("qr: %d %v", rec.Code, rec.Header())
	}
	// Download: the same bytes as a fetch, as an attachment, not recorded
	rec = h.Login.Get("/routing/shadowrocket/1/output")
	out, _ := h.Mod.Shadowrocket.Output(bg(), 1)
	if rec.Code != 200 || rec.Body.String() != string(out) || !strings.Contains(rec.Header().Get("Content-Disposition"), `filename="iphone.conf"`) {
		t.Errorf("download: %d %v", rec.Code, rec.Header())
	}

	// versions: a new one, view, diff, restore
	if rec := h.Login.Post("/routing/shadowrocket/1/base", url.Values{"base": {srBase + "# two\n"}, "note": {"second"}}); rec.Code != 303 || rec.Header().Get("Location") != "/routing/shadowrocket/1?saved=2" {
		t.Fatalf("save: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	has(t, "saved", h.Login.Get("/routing/shadowrocket/1?saved=2").Body.String(), "Saved as base config v2.", ">v2<", "second",
		`href="/routing/shadowrocket/1/versions/2/diff"`, `action="/routing/shadowrocket/1/versions/1/restore"`)
	has(t, "view", h.Login.Get("/routing/shadowrocket/1/versions/1").Body.String(), "iphone: base config v1", "dns-server")
	has(t, "diff", h.Login.Get("/routing/shadowrocket/1/versions/2/diff").Body.String(), "iphone: v1 → v2", "# two")
	if rec := h.Login.Post("/routing/shadowrocket/1/versions/1/restore", nil); rec.Code != 303 || rec.Header().Get("Location") != "/routing/shadowrocket/1?saved=3" {
		t.Errorf("restore: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := h.Login.Get("/routing/shadowrocket/1/versions/9"); rec.Code != 404 {
		t.Errorf("missing version: %d", rec.Code)
	}

	// list and policy
	h.List("Parents")
	has(t, "edit", h.Login.Get("/routing/shadowrocket/1/edit").Body.String(), "Routing list and policy of iphone", `name="policy"`, ">Parents<")
	if rec := h.Login.Post("/routing/shadowrocket/1/edit", url.Values{"list": {"2"}, "policy": {"A,B"}}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "without commas") {
		t.Errorf("bad policy: %d", rec.Code)
	}
	if rec := h.Login.Post("/routing/shadowrocket/1/edit", url.Values{"list": {"2"}, "policy": {"Proxy-Group-A"}}); rec.Code != 303 {
		t.Errorf("edit: %d", rec.Code)
	}
	has(t, "edited", h.Login.Get("/routing/shadowrocket/1").Body.String(), "routing list Parents · policy Proxy-Group-A · base config v3", "Now follows Parents (was Main)")
	// Parents' list page shows the config as a target
	has(t, "target", h.Login.Get("/routing/lists/2").Body.String(), "Targets · 1", `href="/routing/shadowrocket/1"`, "Shadowrocket · fetched", "gets the change on its next fetch")

	// regenerate, disable, enable, delete
	if rec := h.Login.Post("/routing/shadowrocket/1/token", nil); rec.Code != 303 || rec.Header().Get("Location") != "/routing/shadowrocket/1?regenerated=1" {
		t.Errorf("regenerate: %d", rec.Code)
	}
	if rec := h.Login.Post("/routing/shadowrocket/1/disable", nil); rec.Code != 303 {
		t.Errorf("disable: %d", rec.Code)
	}
	has(t, "disabled", h.Login.Get("/routing/shadowrocket/1").Body.String(), "Disabled: the URL answers 404", `action="/routing/shadowrocket/1/enable"`)
	has(t, "disabled row", h.Login.Get("/routing/shadowrocket").Body.String(), "disabled")
	if rec := h.Login.Post("/routing/shadowrocket/1/enable", nil); rec.Code != 303 {
		t.Errorf("enable: %d", rec.Code)
	}
	has(t, "delete page", h.Login.Get("/routing/shadowrocket/1/delete").Body.String(), "Delete iphone?")
	if rec := h.Login.Post("/routing/shadowrocket/1/delete", nil); rec.Code != 303 || rec.Header().Get("Location") != "/routing/shadowrocket" {
		t.Errorf("delete: %d", rec.Code)
	}
	has(t, "empty", h.Login.Get("/routing/shadowrocket").Body.String(), "No Shadowrocket configs yet.")
	if rec := h.Login.Get("/routing/shadowrocket/1"); rec.Code != 404 {
		t.Errorf("deleted page: %d", rec.Code)
	}
}

func TestNewConfigPage(t *testing.T) {
	h := routingtest.New(t)
	body := h.Login.Get("/routing/shadowrocket/new").Body.String()
	has(t, "new", body, "New Shadowrocket config", `name="name"`, "Also the file name: iphone.conf", `name="list"`, `value="PROXY"`,
		`data-code="shadowrocket"`, `name="base_file"`, `name="import_url"`, `formaction="/routing/shadowrocket/import-base"`, "js/editor.bundle.js",
		`enctype="multipart/form-data"`)
	checkNoInline(t, "new", body)

	// pasted text
	rec := h.Login.PostMultipart("/routing/shadowrocket", url.Values{"name": {"iphone"}, "list": {"1"}, "policy": {"PROXY"}, "base": {srBase}}, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/routing/shadowrocket/1?created=1" {
		t.Fatalf("create: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if v, _ := h.Mod.Shadowrocket.Version(bg(), 1, 0); v.Content != srBase {
		t.Errorf("saved %q", v.Content)
	}
	has(t, "created", h.Login.Get("/routing/shadowrocket/1?created=1").Body.String(), "Created. Paste the URL into Shadowrocket.")
	// an uploaded file wins over the field
	rec = h.Login.PostMultipart("/routing/shadowrocket", url.Values{"name": {"ipad"}, "list": {"1"}, "policy": {"PROXY"}, "base": {"ignored"}},
		map[string]sitetest.File{"base_file": {Name: "base.conf", Data: []byte(srBase + "# uploaded\n")}})
	if rec.Code != 303 {
		t.Fatalf("upload: %d", rec.Code)
	}
	if v, _ := h.Mod.Shadowrocket.Version(bg(), 2, 0); v.Content != srBase+"# uploaded\n" {
		t.Errorf("uploaded %q", v.Content)
	}
	// ErrNoRule shown in place, the text kept
	rec = h.Login.PostMultipart("/routing/shadowrocket", url.Values{"name": {"x"}, "list": {"1"}, "policy": {"PROXY"}, "base": {"[General]\nkeep-me = 1\n"}}, nil)
	if rec.Code != 422 {
		t.Fatalf("no rule: %d", rec.Code)
	}
	has(t, "no rule", rec.Body.String(), "No [Rule] section to add the services to.", "keep-me = 1", `value="x"`)
	// Import from URL fills the field; nothing is saved
	u := h.Up.File("base.conf", srBase+"# from the URL\n")
	rec = h.Login.Post("/routing/shadowrocket/import-base", url.Values{"config": {"0"}, "name": {"phone"}, "list": {"1"}, "policy": {"PROXY"}, "import_url": {u}})
	if rec.Code != 200 {
		t.Fatalf("import: %d", rec.Code)
	}
	has(t, "imported", rec.Body.String(), "# from the URL", `value="phone"`, `data-code="shadowrocket"`)
	if cs, _ := h.Mod.Shadowrocket.List(bg()); len(cs) != 2 {
		t.Errorf("import saved something: %d configs", len(cs))
	}
	h.Up.Fail("/files/base.conf", 404)
	rec = h.Login.Post("/routing/shadowrocket/import-base", url.Values{"config": {"0"}, "import_url": {u}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Couldn&#39;t read it: HTTP 404") {
		t.Errorf("failing import: %d", rec.Code)
	}
	if rec := h.Login.Post("/routing/shadowrocket/import-base", url.Values{"config": {"0"}, "import_url": {"file:///etc/passwd"}}); rec.Code != 422 {
		t.Errorf("file URL: %d", rec.Code)
	}

	// Edit base config: the editor page, saving what the textarea posts
	body = h.Login.Get("/routing/shadowrocket/1/base").Body.String()
	has(t, "edit base", body, "Edit base config of iphone", `data-code="shadowrocket"`, "js/editor.bundle.js", "RULE-SET,https://example.com/ads.list")
	checkNoInline(t, "edit base", body)
	rec = h.Login.PostMultipart("/routing/shadowrocket/1/base", url.Values{"base": {"[General]\n"}}, nil)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "No [Rule] section") {
		t.Errorf("edit no rule: %d", rec.Code)
	}
	rec = h.Login.PostMultipart("/routing/shadowrocket/1/base", url.Values{"base": {srBase + "# edited\r\n"}}, nil)
	if rec.Code != 303 {
		t.Fatalf("edit: %d", rec.Code)
	}
	if v, _ := h.Mod.Shadowrocket.Version(bg(), 1, 0); v.Number != 2 || v.Content != srBase+"# edited\r\n" {
		t.Errorf("edited: %+v", v)
	}
	if rec := h.Login.PostMultipart("/routing/shadowrocket/1/base", url.Values{"base": {srBase + "# edited\r\n"}}, nil); rec.Header().Get("Location") != "/routing/shadowrocket/1?unchanged=1" {
		t.Errorf("unchanged: %s", rec.Header().Get("Location"))
	}
}

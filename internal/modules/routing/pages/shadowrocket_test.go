package pages_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"golang.org/x/crypto/ssh"
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
		"http://<wbr>proxier.test/<wbr>r/<wbr>••••••••••••••••/<wbr>iphone.conf", `hx-get="/routing/shadowrocket/1/url"`, `hx-get="/routing/shadowrocket/1/qr"`,
		"In Shadowrocket: Config → + → paste the URL → Use config", `data-key="y"`,
		"What the phone gets", `class="sr-line sr-ours"`, "DOMAIN-SUFFIX,anthropic.com,PROXY", "RULE-SET,https://example.com/ads.list,REJECT",
		`href="/routing/shadowrocket/1/output"`, "Download iphone.conf",
		"Base config versions", `>v1 <span class="pine normal">· current</span>`, `href="/routing/shadowrocket/1/versions/1"`,
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
	has(t, "saved", h.Login.Get("/routing/shadowrocket/1?saved=2").Body.String(), "Saved as base config v2.", `>v2 <span class="pine normal">· current</span>`, "second",
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
	has(t, "new editor", body, `form="sr-form" class="pc-btn pc-pri" data-action="shadowrocket.create" data-code-save`, `class="sr-form code-fill"`,
		`<div class="sr-strip top">`, `<section class="code-frame"`, `data-code-full`)

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
	// the page is its editor: the code fills the window (not the 560 px .form column), Save stays
	// in the title block (⌘S presses it), and the band holds Wrap lines and Full screen
	if !regexp.MustCompile(`(?s)<div class="actions"><button type="submit" form="sr-form" class="pc-btn pc-pri" data-action="shadowrocket.save" data-code-save>Save</button>`).MatchString(body) {
		t.Error("Save is not in the title block")
	}
	has(t, "edit base editor", body, `id="sr-form"`, `class="sr-form code-fill"`, `<section class="code-frame"`, `data-code-wrap`, `data-code-full`,
		`data-action="code.full"`, ">Wrap lines<", ">Full screen<", "In a code editor", "Leave the code, then full screen")
	if strings.Contains(body, `class="form sr-form`) {
		t.Error("the base config sits in the narrow form column")
	}
	checkNoInline(t, "edit base", body)
	rec = h.Login.PostMultipart("/routing/shadowrocket/1/base", url.Values{"base": {"[General]\n"}}, nil)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `<p class="code-err" role="alert">No [Rule] section`) {
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

// Parts of an area body that sat on each other (the link page's URL and QR
// areas had the same): the config's URL, its buttons, QR, the code and the
// note; a router test's lines and It matches, continue; a taken service's text
// and its button. Their bodies are stacked with 12 px between parts (app.css is
// checked by TestLinkPartsAreSpaced), and Hide QR takes the place of QR with
// the code under it.
func TestAreaPartsAreSpaced(t *testing.T) {
	tight := func(s string) string { return strings.TrimSpace(regexp.MustCompile(`>\s+<`).ReplaceAllString(s, "><")) }
	h, _, _ := srSetup(t)
	page := tight(h.Login.Get("/routing/shadowrocket/1").Body.String())
	if !regexp.MustCompile(`<section class="area" aria-label="URL">.*?<div class="area-body stacked"><div class="urlfield" id="sr-url"><code class="masked">(?:[^<]|<wbr>)+</code><span class="url-btns"><button [^>]*>Reveal</button><button [^>]*>Copy</button></span></div><div id="sr-qr" class="qrslot"><button [^>]*>Show QR</button></div><p class="sm subtle">In Shadowrocket:`).MatchString(page) {
		t.Error("the config's URL area isn't the URL with its buttons, QR and the note, stacked")
	}
	if shown := tight(h.Login.Get("/routing/shadowrocket/1/qr").Body.String()); !regexp.MustCompile(`^<div id="sr-qr" class="qrslot"><button [^>]*>Hide QR</button><svg class="qr"`).MatchString(shown) {
		t.Errorf("shown: %.200s", shown)
	}
	if revealed := tight(h.Login.Get("/routing/shadowrocket/1/url").Body.String()); !regexp.MustCompile(`^<div class="urlfield" id="sr-url"><code>(?:[^<]|<wbr>)+</code><span class="url-btns"><button [^>]*>Hide</button><button [^>]*>Copy</button></span></div>$`).MatchString(revealed) {
		t.Errorf("revealed: %s", revealed)
	}

	// a router test waiting at the jump host's fingerprint
	key, _, _ := h.App.SSH.PublicKey(bg())
	k, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(key)))
	c := routingtest.Conn(routerostest.New(t, k), routerostest.Jump(t, k))
	loc := location(t, h.Login.Post("/routing/routers/test", connForm(c, "Parents", 1)))
	h.StartJobs()
	h.Settle()
	page = tight(h.Login.Get(loc).Body.String())
	if !regexp.MustCompile(`id="test-area".*?<div class="area-body stacked"><p>First contact with the jump host .*?</p><p><code class="fp">.*?<button [^>]*>It matches, continue</button>`).MatchString(page) {
		t.Error("the test area isn't stacked")
	}

	// a service whose tag is taken
	h.Up.Site("main", "ai", "anthropic", "anthropic.com")
	page = tight(h.Login.Post("/routing/services", url.Values{"selector": {"iplist:anthropic"}}).Body.String())
	if !regexp.MustCompile(`<section class="area" aria-label="anthropic already exists">.*?<div class="area-body stacked"><dl class="kv">.*?</dl><p>.*?</p><form [^>]*class="form-actions">`).MatchString(page) {
		t.Error("the taken area isn't stacked")
	}
}

// The config page (issue 016): the URL across the page above the columns,
// breaking only after a slash; the download in the preview's title band, not
// after a preview that may run thousands of lines; "current" beside the
// version's number, not against its buttons; a fetch's address and user
// agent on one line each, the whole value in a title; the action rows
// sharing one button column.
func TestConfigPageHoldsTogether(t *testing.T) {
	tight := func(s string) string { return strings.TrimSpace(regexp.MustCompile(`>\s+<`).ReplaceAllString(s, "><")) }
	h, _, token := srSetup(t)
	ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0.1 Safari/605.1.15"
	h.Site.Do(sitetest.Req{Path: "/r/" + token + "/iphone.conf", Addr: "[2001:db8:85a3::8a2e:370:7334]:5000", Header: http.Header{"User-Agent": {ua}}})
	h.Site.Do(sitetest.Req{Path: "/r/" + token + "/iphone.conf", Addr: "198.51.100.23:5000", Header: http.Header{"User-Agent": {""}}})
	if _, _, err := h.Mod.Shadowrocket.SaveBase(bg(), 1, srBase+"\n", "imported from mtvpn", "admin"); err != nil {
		t.Fatal(err)
	}
	page := tight(h.Login.Get("/routing/shadowrocket/1").Body.String())

	if !regexp.MustCompile(`</div></div><section class="area" aria-label="URL"><div class="pc-title">.*?</section><div class="subcols sr-cols"><div class="col-l"><section class="area" aria-label="What the phone gets">`).MatchString(page) {
		t.Error("the URL area isn't across the page above the preview's column")
	}
	if !strings.Contains(page, `<code class="masked">http://<wbr>proxier.test/<wbr>r/<wbr>••••••••••••••••/<wbr>iphone.conf</code>`) {
		t.Error("the masked URL may break inside a word")
	}
	if revealed := h.Login.Get("/routing/shadowrocket/1/url").Body.String(); !strings.Contains(revealed, "<code>http://<wbr>proxier.test/<wbr>r/<wbr>"+token+"/<wbr>iphone.conf</code>") ||
		!strings.Contains(revealed, `data-copy="http://proxier.test/r/`+token+`/iphone.conf"`) {
		t.Errorf("revealed: %s", revealed)
	}
	if !regexp.MustCompile(`<section class="area" aria-label="What the phone gets"><div class="pc-title"><h2>What the phone gets</h2><span class="note">[^<]*</span><span class="flex1"></span><a class="pc-btn pc-sm" href="/routing/shadowrocket/1/output" download data-action="shadowrocket.download">Download iphone.conf</a></div><div class="sr-preview">(<div class="sr-line[^"]*"><span class="ln">\d+</span><span class="tx">[^<]*</span></div>)+</div></section></div>`).MatchString(page) {
		t.Error("the preview's download isn't in its title band, or something follows the preview")
	}

	if !regexp.MustCompile(`<div class="row srver"><span class="bold">v2 <span class="pine normal">· current</span></span><span class="col"><span class="sm subtle">[^<]+</span><span class="sm cut" title="imported from mtvpn">imported from mtvpn</span></span><span class="sr-acts"><a class="pc-btn pc-sm" [^>]*>View</a>`).MatchString(page) {
		t.Error("the current version's row isn't its number with current, its time over its note, then its buttons")
	}
	if regexp.MustCompile(`<span class="sr-acts">[^/]*current`).MatchString(page) {
		t.Error("current sits against the version's buttons")
	}

	for _, want := range []string{
		`<div class="row srfetch"><span class="subtle cut">[^<]+</span><span class="cut" title="2001:db8:85a3::8a2e:370:7334">2001:db8:85a3::8a2e:370:7334</span><span class="ua subtle cut" title="` + regexp.QuoteMeta(ua) + `">`,
		`<div class="row srfetch"><span class="subtle cut">[^<]+</span><span class="cut" title="198\.51\.100\.23">198\.51\.100\.23</span></div>`, // no user agent: no empty cell
	} {
		if !regexp.MustCompile(want).MatchString(page) {
			t.Errorf("fetch rows: no %s", want)
		}
	}

	if !regexp.MustCompile(`<section class="area" aria-label="Actions"><div class="pc-title">.*?</div><div class="act-rows">(<div class="row act">.*?</div>){4}</div></section>`).MatchString(page) {
		t.Error("the action rows don't share a button column")
	}
}

package pages_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestImportPages(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	h.Up.V2fly("youtube", "youtube.com\n")
	list := h.Up.File("mtvpn-main.txt", "v2fly:youtube\nx=ftp://example.com/a\n")
	b := h.Up.File("base.conf", srBase)
	text := "services:\n  - anthropic\nservice_lists:\n  - " + list + "\nshadowrocket_base: " + b + "\nshadowrocket_password: hunter2\n"

	has(t, "lists page", h.Login.Get("/routing/lists").Body.String(), `href="/routing/import"`, "Import from mtvpn…")
	body := h.Login.Get("/routing/import").Body.String()
	has(t, "paste", body, "Import from mtvpn", "Moves your mtvpn.yaml setup in once.", "1 · Paste", "2 · Preview", "3 · Result",
		`name="text"`, `name="file"`, `aria-current="step"`)
	checkNoInline(t, "paste", body)
	rec := h.Login.PostMultipart("/routing/import", url.Values{"text": {"services\n"}}, nil)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Line 1 is not &#39;key: value&#39;.") {
		t.Errorf("broken: %d", rec.Code)
	}
	// step 2, from the uploaded file
	rec = h.Login.PostMultipart("/routing/import", url.Values{}, map[string]sitetest.File{"file": {Name: "mtvpn.yaml", Data: []byte(text)}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/routing/import/1" {
		t.Fatalf("preview: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body = h.Login.Get("/routing/import/1").Body.String()
	has(t, "preview", body, "3 selectors · 2 new · 0 already exist, reused · 1 can&#39;t be read",
		"ignored keys: shadowrocket_password · never stored", "v2fly:anthropic", "tag anthropic", "mtvpn-main.txt", "can&#39;t be read",
		`name="include" value="0" checked`, `name="list"`, "shadowrocket_base found · 6 lines, has a [Rule] section",
		`name="config_create" value="1" checked`, `value="iphone"`, "Import 2 services and 1 config", "Main&#39;s targets re-sync after the import.")
	if strings.Contains(body, "hunter2") {
		t.Error("the preview holds the password")
	}
	checkNoInline(t, "preview", body)

	// step 3: importing (polling), then the result
	form := url.Values{"include": {"0", "1"}, "list": {"1"}, "config_create": {"1"}, "config_name": {"iphone"}, "config_list": {"1"}}
	if rec := h.Login.Post("/routing/import/1", form); rec.Code != 303 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	body = h.Login.Get("/routing/import/1").Body.String()
	has(t, "importing", body, "Importing…", `hx-get="/routing/import/1/status"`, `hx-trigger="every 2s"`, "Resolve and create services")
	rec = h.Site.Do(sitetest.Req{Path: "/routing/import/1/status", Cookies: []*http.Cookie{h.Login.Cookie}, Header: http.Header{"Hx-Request": {"true"}}})
	if rec.Code != 200 || rec.Header().Get("Hx-Refresh") != "" {
		t.Errorf("status while running: %d %v", rec.Code, rec.Header())
	}
	h.StartJobs()
	h.Drain()
	rec = h.Site.Do(sitetest.Req{Path: "/routing/import/1/status", Cookies: []*http.Cookie{h.Login.Cookie}, Header: http.Header{"Hx-Request": {"true"}}})
	if rec.Header().Get("Hx-Refresh") != "true" {
		t.Error("the poll doesn't reload after the end")
	}
	body = h.Login.Get("/routing/import/1").Body.String()
	has(t, "result", body, "Created 2 · reused 0 · converted 0 · skipped 0", `href="/routing/services/1"`, "anthropic", "youtube",
		"Created iphone.", `href="/routing/shadowrocket/1"`, "Point Shadowrocket at the new URL:", "Add your routers")
	// running it again goes to the result
	if rec := h.Login.Post("/routing/import/1", form); rec.Code != 303 {
		t.Errorf("again: %d", rec.Code)
	}
	// the list page shows the config as a target
	has(t, "list page", h.Login.Get("/routing/lists/1").Body.String(), "Targets · 1", ">iphone<", "never fetched")
	if rec := h.Login.Get("/routing/import/99"); rec.Code != 404 {
		t.Errorf("missing: %d", rec.Code)
	}
}

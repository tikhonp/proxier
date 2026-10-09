package shadowrocket_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/httpx"
)

func TestPublicFetch(t *testing.T) {
	h, id := setup(t)
	p := pathOf(t, h, id)
	rec := fetch(h, "GET", p)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("GET: %d %v", rec.Code, rec.Header())
	}
	want := "[General]\ndns-server = system\n\n[Rule]\nRULE-SET,https://example.com/ads.list,REJECT\n\n" +
		"# Services from routing list \"Main\", by Proxier (2026-10-08 12:00 UTC)\n\n# anthropic\nDOMAIN-SUFFIX,anthropic.com,PROXY\nDOMAIN-SUFFIX,claude.ai,PROXY\nFINAL,DIRECT\n"
	if rec.Body.String() != want {
		t.Errorf("body:\n%s", rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a public answer sets a cookie")
	}
	out, _ := h.Mod.Shadowrocket.Output(bg(), id)
	if string(out) != want {
		t.Error("Output differs from what the phone gets")
	}
	var f struct {
		IP, UA string
	}
	if err := h.App.DB.R.QueryRow(`SELECT ip, user_agent FROM routing_shadowrocket_fetches`).Scan(&f.IP, &f.UA); err != nil || f.IP != "198.51.100.23" || f.UA != phoneUA {
		t.Errorf("fetch: %+v %v", f, err)
	}
	c, _ := h.Mod.Shadowrocket.Get(bg(), id)
	if c.LastFetchUA != phoneUA || !c.LastFetchAt.Equal(h.Now) {
		t.Errorf("last fetch: %+v", c)
	}
	fs, _ := h.Mod.Shadowrocket.Fetches(bg(), id, 0, 50)
	if len(fs) != 1 || fs[0].IP != "198.51.100.23" {
		t.Errorf("fetch log: %+v", fs)
	}
	// HEAD: the same answer without a body, not recorded
	rec = fetch(h, "HEAD", p)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("HEAD: %d %d", rec.Code, rec.Body.Len())
	}
	if fetches(t, h) != 1 {
		t.Errorf("%d fetches after HEAD", fetches(t, h))
	}
	// a long user agent is cut to 256 characters
	h.Site.Do(reqUA(p, strings.Repeat("ш", 300)))
	var ua string
	_ = h.App.DB.R.Get(&ua, `SELECT user_agent FROM routing_shadowrocket_fetches ORDER BY id DESC LIMIT 1`)
	if len([]rune(ua)) != 256 {
		t.Errorf("ua of %d characters", len([]rune(ua)))
	}
	// the preview renders the same and records nothing
	if rec := h.Login.Get("/routing/shadowrocket/1"); rec.Code != 200 || fetches(t, h) != 2 {
		t.Errorf("page: %d, %d fetches", rec.Code, fetches(t, h))
	}
}

func TestUnknownTokenIs404(t *testing.T) {
	h, id := setup(t)
	p := pathOf(t, h, id)
	token := strings.Split(p, "/")[2]
	plain := fetch(h, "GET", "/r/"+strings.Repeat("a", 43)+"/nothing.conf").Body.String()
	for _, path := range []string{
		"/r/" + strings.Repeat("A", 43) + "/iphone.conf", // unknown
		"/r/" + token + "/ipad.conf",                     // wrong file
		"/r/" + token + "/iphone",                        // wrong file
		"/r/short/iphone.conf",                           // bad shape
		"/r/" + token[:30] + "!" + "/iphone.conf",        // bad shape
		"/r/" + token,                                    // no file
	} {
		rec := fetch(h, "GET", path)
		if rec.Code != 404 {
			t.Errorf("%s: %d", path, rec.Code)
		}
		if rec.Body.String() != plain {
			t.Errorf("%s: not the plain public 404: %q", path, rec.Body.String())
		}
	}
	if fetches(t, h) != 0 {
		t.Errorf("%d fetches recorded", fetches(t, h))
	}
}

func TestFetchSeesNewSnapshot(t *testing.T) {
	h, id := setup(t)
	p := pathOf(t, h, id)
	h.Advance(16*time.Hour + 5*time.Minute) // 04:05 the next day
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\nclaude.com\n")
	out, err := h.Mod.Refresh.Refresh(bg(), 1, false, "admin")
	if err != nil {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	h.Advance(time.Minute)
	body := fetch(h, "GET", p).Body.String()
	if !strings.Contains(body, "DOMAIN-SUFFIX,claude.com,PROXY\n") || !strings.Contains(body, "(2026-10-09 04:06 UTC)") {
		t.Errorf("04:06 fetch:\n%s", body)
	}
}

func TestTokenStaysOnItsPage(t *testing.T) {
	var log bytes.Buffer
	h, id := setup(t, routingtest.LogTo(&log))
	p := pathOf(t, h, id)
	token := strings.Split(p, "/")[2]
	fetch(h, "GET", p)
	fetch(h, "GET", "/r/"+token+"/wrong.conf")
	if err := h.Mod.Shadowrocket.RegenerateToken(bg(), id, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Shadowrocket.SetEnabled(bg(), id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	var payloads []string
	if err := h.App.DB.R.Select(&payloads, `SELECT payload FROM events`); err != nil {
		t.Fatal(err)
	}
	for _, pl := range payloads {
		if strings.Contains(pl, token) {
			t.Errorf("an event holds the token: %s", pl)
		}
	}
	if strings.Contains(log.String(), token) {
		t.Errorf("the log holds the token:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "/r/•••/iphone.conf") {
		t.Errorf("no masked request line:\n%s", log.String())
	}
	if httpx.MaskPath(p) != "/r/•••/iphone.conf" {
		t.Errorf("mask: %s", httpx.MaskPath(p))
	}
	cur := strings.Split(pathOf(t, h, id), "/")[2]
	for _, page := range []string{"/routing/shadowrocket", "/routing/lists/1", "/routing/lists", "/activity"} {
		body := h.Login.Get(page).Body.String()
		if strings.Contains(body, token) || strings.Contains(body, cur) {
			t.Errorf("%s holds a token", page)
		}
	}
	hits, _ := h.Mod.Search(bg(), "iph", 10)
	for _, hit := range hits {
		if strings.Contains(hit.Href+hit.Meta, cur) {
			t.Errorf("search hit holds the token: %+v", hit)
		}
	}
	if len(hits) != 1 || hits[0].Href != "/routing/shadowrocket/1" {
		t.Errorf("search: %+v", hits)
	}
	// the config page holds it (Copy), masked in sight
	if body := h.Login.Get("/routing/shadowrocket/1").Body.String(); !strings.Contains(body, cur) || !strings.Contains(body, "/r/••••••••••••••••/iphone.conf") {
		t.Error("the config page lacks the URL or its masked form")
	}
}

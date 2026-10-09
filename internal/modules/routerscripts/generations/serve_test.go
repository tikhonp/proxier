package generations_test

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestRouterFetchesOnce(t *testing.T) {
	h := rscriptstest.New(t)
	gid := generated(t, h)
	_, token := createURL(t, h, gid)
	res := fetch(h, http.MethodGet, token, "Mikrotik/7.24.5 Fetch")
	download := h.Login.Get("/router-scripts/generations/1/download")
	if res.Code != http.StatusOK || res.Body != download.Body.String() || !strings.Contains(res.Body, ":local subUrl \"https://proxier.test/s/") {
		t.Fatalf("fetch: %d", res.Code)
	}
	if res.Header.Get("Content-Type") != "text/plain; charset=utf-8" || res.Header.Get("Content-Disposition") != `attachment; filename="fresh-router-Parents-v1.rsc"` ||
		res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Set-Cookie") != "" {
		t.Errorf("headers: %v", res.Header)
	}
	ev := h.Events("routerscript.fetched")
	if len(ev) != 1 || ev[0].Actor != "system" || ev[0].Subject != generations.Subject(gid) || ev[0].Payload["ip"] != "198.51.100.4" ||
		ev[0].Payload["user_agent"] != "Mikrotik/7.24.5 Fetch" {
		t.Fatalf("event: %+v", ev)
	}
	rows := urlRows(t, h)
	if rows[0].State != "used" || rows[0].Token != nil {
		t.Fatalf("row: %+v", rows)
	}
	list, _ := h.Mod.Generations.FetchURLs(bg, gid)
	if list[0].IP != "198.51.100.4" || list[0].UserAgent != "Mikrotik/7.24.5 Fetch" || !list[0].EndedAt.Equal(h.Now) {
		t.Fatalf("history: %+v", list[0])
	}
	if res := fetch(h, http.MethodGet, token, "Mikrotik/7.24.5 Fetch"); res.Code != http.StatusNotFound || res.Body != "Not Found\n" {
		t.Fatalf("second fetch: %d %q", res.Code, res.Body)
	}
	if len(h.Events("routerscript.fetched")) != 1 {
		t.Fatal("the second fetch recorded")
	}
	if _, _, ok, _ := h.Mod.Generations.Live(bg, gid); ok {
		t.Fatal("a used URL is live")
	}
	// a long user agent is cut to 256 characters
	_, token = createURL(t, h, gid)
	fetch(h, http.MethodGet, token, strings.Repeat("ж", 300))
	if ev := h.Events("routerscript.fetched"); len([]rune(ev[1].Payload["user_agent"].(string))) != 256 {
		t.Errorf("user agent kept whole")
	}
}

func TestFetchRefusesWithoutUsing(t *testing.T) {
	h := rscriptstest.New(t)
	gid := generated(t, h)
	_, token := createURL(t, h, gid)
	for _, bad := range []string{"short", token[:42], token[:42] + "!", token + "A", strings.Repeat("ы", 43)} {
		if res := fetch(h, http.MethodGet, bad, ""); res.Code != http.StatusNotFound || res.Body != "Not Found\n" {
			t.Errorf("%q: %d %q", bad, res.Code, res.Body)
		}
	}
	unknown := strings.Repeat("A", 43)
	if res := fetch(h, http.MethodGet, unknown, ""); res.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", res.Code)
	}
	for _, m := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
		if res := fetch(h, m, token, ""); res.Code != http.StatusNotFound {
			t.Errorf("%s: %d", m, res.Code)
		}
	}
	if rows := urlRows(t, h); rows[0].State != "waiting" {
		t.Fatalf("used by a refused request: %+v", rows)
	}
	if len(h.Events("routerscript.fetched")) != 0 {
		t.Fatal("recorded")
	}
	if res := fetch(h, http.MethodGet, token, ""); res.Code != http.StatusOK {
		t.Fatalf("still works: %d", res.Code)
	}
}

func TestConcurrentFetchesOneWins(t *testing.T) {
	h := rscriptstest.New(t)
	gid := generated(t, h)
	_, token := createURL(t, h, gid)
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = fetch(h, http.MethodGet, token, "").Code
		}()
	}
	wg.Wait()
	if codes[0]+codes[1] != http.StatusOK+http.StatusNotFound {
		t.Fatalf("codes: %v", codes)
	}
	if n := len(h.Events("routerscript.fetched")); n != 1 {
		t.Fatalf("%d events", n)
	}
}

// syncBuffer is a log both the test and the app's goroutines touch.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestFetchKeepsTheTokenOut(t *testing.T) {
	var log syncBuffer
	h := rscriptstest.New(t, rscriptstest.LogTo(&log))
	gid := generated(t, h)
	_, token := createURL(t, h, gid)

	page := h.Login.Get("/router-scripts/generations/1")
	body := page.Body.String()
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Error("the page is stored")
	}
	// only in the Copy buttons: the URL, the fetch line, both lines
	if n, copies := strings.Count(body, token), strings.Count(body, `data-copy="http://proxier.test/f/`+token)+
		strings.Count(body, `data-copy="/tool fetch url=&#34;http://proxier.test/f/`+token); n != 3 || copies != 3 {
		t.Errorf("the token is %d times on the page, %d in Copy buttons", n, copies)
	}
	reveal := h.Login.Get("/router-scripts/generations/1/fetch-url/reveal")
	if reveal.Header().Get("Cache-Control") != "no-store" || !strings.Contains(reveal.Body.String(), "<code>http://proxier.test/f/"+token+"</code>") {
		t.Fatalf("reveal: %q %s", reveal.Header().Get("Cache-Control"), reveal.Body.String())
	}
	if hide := h.Login.Get("/router-scripts/generations/1/fetch-url/reveal?hide=1").Body.String(); strings.Contains(hide, "<code>http://proxier.test/f/"+token) {
		t.Error("hide shows the URL")
	}

	if res := fetch(h, http.MethodGet, token, "Mikrotik/7.24.5 Fetch"); res.Code != http.StatusOK {
		t.Fatal(res.Code)
	}
	fetch(h, http.MethodGet, token, "")
	if l := log.String(); !strings.Contains(l, "/f/•••") || strings.Contains(l, token) {
		t.Errorf("the request log: %s", l)
	}
	all := h.Events("")
	for _, e := range all {
		for k, v := range e.Payload {
			if s, ok := v.(string); ok && strings.Contains(s, token) {
				t.Errorf("%s.%s holds the token", e.Type, k)
			}
		}
	}
	var payloads []string
	if err := h.App.DB.R.Select(&payloads, `SELECT coalesce(payload, '') FROM jobs`); err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if strings.Contains(p, token) {
			t.Error("a job payload holds the token")
		}
	}
	loc := h.App.I18n.Localizer(i18n.EN, nil)
	for _, e := range all {
		msg, ok, err := h.Mod.RenderNotification(bg, e, loc)
		if err != nil || ok && (strings.Contains(msg.Title+msg.Body, token)) {
			t.Errorf("notification of %s: %+v %v", e.Type, msg, err)
		}
	}
}

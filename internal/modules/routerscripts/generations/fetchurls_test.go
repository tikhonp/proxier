package generations_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// generated makes a script from the annotated fixture and generates
// "Parents" from it with a new link in Family and no router; the
// generation's id.
func generated(t *testing.T, h *rscriptstest.Harness) int64 {
	t.Helper()
	id := h.Script("fresh-router", paramstest.Annotated())
	f := form(t, h, id).Form
	f.RouterName, f.Router.Mode = "Parents", generations.RouterNone
	f.Link.SubscriptionID = 1
	return generate(t, h, id, f)
}

// createURL creates a fetch URL as the admin; its token.
func createURL(t *testing.T, h *rscriptstest.Harness, gid int64) (generations.FetchURL, string) {
	t.Helper()
	u, link, err := h.Mod.Generations.CreateFetchURL(bg, gid, events.ActorAdmin)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "http://proxier.test/f/"
	if !strings.HasPrefix(link, prefix) {
		t.Fatalf("URL %s", link)
	}
	return u, strings.TrimPrefix(link, prefix)
}

// fetch requests a fetch URL's path as a router would.
func fetch(h *rscriptstest.Harness, method, token, ua string) *httpResult {
	hdr := http.Header{}
	if ua != "" {
		hdr.Set("User-Agent", ua)
	}
	res := h.Site.Do(sitetest.Req{Method: method, Path: "/f/" + token, Header: hdr, Addr: "198.51.100.4:51000"})
	return &httpResult{Code: res.Code, Body: res.Body.String(), Header: res.Header()}
}

type httpResult struct {
	Code   int
	Body   string
	Header http.Header
}

// urlRow is a fetch URL row as stored.
type urlRow struct {
	ID     int64  `db:"id"`
	State  string `db:"state"`
	Token  []byte `db:"token"`
	Lookup []byte `db:"token_lookup"`
}

func urlRows(t *testing.T, h *rscriptstest.Harness) []urlRow {
	t.Helper()
	var out []urlRow
	if err := h.App.DB.R.Select(&out, `SELECT id, state, token, token_lookup FROM rscripts_fetch_urls ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateFetchURL(t *testing.T) {
	h := rscriptstest.New(t)
	gid := generated(t, h)
	u, token := createURL(t, h, gid)
	if len(token) != 43 || u.State != generations.FetchWaiting || !u.ExpiresAt.Equal(h.Now.Add(time.Hour)) || u.CreatedBy != "admin" {
		t.Fatalf("fetch URL: %+v %q", u, token)
	}
	ev := h.Events("routerscript.fetch_url_created")
	if len(ev) != 1 || ev[0].Subject != generations.Subject(gid) || ev[0].Actor != "admin" ||
		ev[0].Payload["expires"] != "2026-10-09T13:00:00.000Z" || ev[0].Payload["replaced"] != false {
		t.Fatalf("event: %+v", ev)
	}
	rows := urlRows(t, h)
	if len(rows) != 1 || len(rows[0].Token) == 0 || !bytes.Equal(rows[0].Lookup, h.App.Vault.Lookup(token)) ||
		bytes.Contains(rows[0].Token, []byte(token)) {
		t.Fatalf("stored: %+v", rows)
	}
	opened, err := h.App.Vault.OpenString(rows[0].Token, "fetch_url:1:token")
	if err != nil || opened != token {
		t.Fatalf("sealed as fetch_url:1:token: %v", err)
	}
	live, link, ok, err := h.Mod.Generations.Live(bg, gid)
	if err != nil || !ok || live.ID != u.ID || link != "http://proxier.test/f/"+token {
		t.Fatalf("live: %+v %s %v %v", live, link, ok, err)
	}
	fetchCmd, imp := generations.Commands(link, "fresh-router")
	if fetchCmd != `/tool fetch url="`+link+`" dst-path=fresh-router.rsc` || imp != "/import fresh-router.rsc" {
		t.Errorf("commands: %s / %s", fetchCmd, imp)
	}
	// an archived script's generation still gets one
	if err := h.Mod.Scripts.Archive(bg, 1, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Mod.Generations.CreateFetchURL(bg, gid, "admin"); err != nil {
		t.Fatalf("archived: %v", err)
	}
	if _, _, err := h.Mod.Generations.CreateFetchURL(bg, 99, "admin"); err != generations.ErrNotFound {
		t.Fatalf("unknown generation: %v", err)
	}
}

func TestNewFetchURLEndsTheOld(t *testing.T) {
	h := rscriptstest.New(t)
	gid := generated(t, h)
	_, first := createURL(t, h, gid)
	h.Advance(10 * time.Minute)
	_, second := createURL(t, h, gid)
	rows := urlRows(t, h)
	if len(rows) != 2 || rows[0].State != "replaced" || rows[0].Token != nil || rows[0].Lookup != nil || rows[1].State != "waiting" {
		t.Fatalf("rows: %+v", rows)
	}
	if res := fetch(h, http.MethodGet, first, "Mikrotik/7.24.5 Fetch"); res.Code != http.StatusNotFound {
		t.Fatalf("the old URL: %d", res.Code)
	}
	ev := h.Events("routerscript.fetch_url_created")
	if len(ev) != 2 || ev[1].Payload["replaced"] != true {
		t.Fatalf("events: %+v", ev)
	}
	if len(h.Events("routerscript.fetch_url_expired")) != 0 {
		t.Fatal("a replaced URL didn't expire")
	}
	list, err := h.Mod.Generations.FetchURLs(bg, gid)
	if err != nil || len(list) != 2 || list[0].State != "waiting" || list[1].State != "replaced" || !list[1].EndedAt.Equal(h.Now) {
		t.Fatalf("history: %+v %v", list, err)
	}
	if res := fetch(h, http.MethodGet, second, ""); res.Code != http.StatusOK {
		t.Fatalf("the new URL: %d", res.Code)
	}
}

func TestExpiredWhileCreating(t *testing.T) {
	h := rscriptstest.New(t)
	gid := generated(t, h)
	createURL(t, h, gid)
	h.Advance(time.Hour + time.Minute)
	if _, _, ok, _ := h.Mod.Generations.Live(bg, gid); ok {
		t.Fatal("live after its hour")
	}
	createURL(t, h, gid)
	rows := urlRows(t, h)
	if len(rows) != 2 || rows[0].State != "expired" || rows[0].Token != nil || rows[1].State != "waiting" {
		t.Fatalf("rows: %+v", rows)
	}
	exp := h.Events("routerscript.fetch_url_expired")
	if len(exp) != 1 || exp[0].Payload["expired"] != "2026-10-09T13:00:00.000Z" || exp[0].Subject != generations.Subject(gid) {
		t.Fatalf("expired: %+v", exp)
	}
	if ev := h.Events("routerscript.fetch_url_created"); len(ev) != 2 || ev[1].Payload["replaced"] != false {
		t.Fatalf("created: %+v", ev)
	}
}

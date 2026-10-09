package pages_test

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"golang.org/x/crypto/ssh"
)

func connForm(c routers.Connection, name string, list int64) url.Values {
	f := url.Values{
		"name": {name}, "list": {strconv.FormatInt(list, 10)}, "host": {c.Host}, "port": {strconv.Itoa(c.Port)}, "user": {c.User},
		"jump_host": {c.JumpHost}, "jump_port": {strconv.Itoa(c.JumpPort)}, "jump_user": {c.JumpUser}, "first_hop": {"direct"},
		"list_name": {c.Names.List}, "forwarder": {c.Names.Forwarder},
	}
	return f
}

// location is a redirect's target.
func location(t *testing.T, rec interface {
	Result() *http.Response
}) string {
	t.Helper()
	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther && res.StatusCode != http.StatusFound {
		t.Fatalf("not a redirect: %d", res.StatusCode)
	}
	return res.Header.Get("Location")
}

func testIDOf(t *testing.T, loc string) int64 {
	t.Helper()
	u, _ := url.Parse(loc)
	n, err := strconv.ParseInt(u.Query().Get("test"), 10, 64)
	if err != nil {
		t.Fatalf("no test in %s", loc)
	}
	return n
}

func TestAddRouterPage(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	key, _, _ := h.App.SSH.PublicKey(bg())
	key = strings.TrimSpace(key)

	page := h.Login.Get("/routing/routers/new").Body.String()
	has(t, "add page", page, "<h1>Add router</h1>", html.EscapeString(key),
		html.EscapeString("echo '"+key+"' >> ~/.ssh/authorized_keys"),
		"/user group add name=proxier policy=read,write,ftp,ssh", "/user add name=proxier group=proxier",
		html.EscapeString(`/user ssh-keys add user=proxier key="`+key+`"`), `data-copy-from="#key-router"`,
		`formaction="/routing/routers/test"`, "Test connection", "Save and sync", `value="to_vpn_list"`, `value="vpn-doh"`, `value="proxier"`,
		"to verify on the first real router")
	checkNoInline(t, "add page", page)

	// Test connection of a router behind a jump host: the test area polls while it runs
	k, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key))
	r := routerostest.New(t, k)
	jump := routerostest.Jump(t, k)
	c := routingtest.Conn(r, jump)
	loc := location(t, h.Login.Post("/routing/routers/test", connForm(c, "Parents", 1)))
	if !strings.HasPrefix(loc, "/routing/routers/new?test=") || !strings.Contains(loc, "name=Parents") {
		t.Fatalf("back to the form: %s", loc)
	}
	id := testIDOf(t, loc)
	running := hx(h, "GET", "/routing/routers/tests/"+strconv.FormatInt(id, 10)+"?name=Parents&list=1", nil)
	has(t, "running test", running, `hx-trigger="every 1s"`, "Connecting…")
	h.StartJobs()
	h.Settle()
	page = h.Login.Get(loc).Body.String()
	has(t, "confirm", page, "First contact with the jump host "+c.JumpAddress(), ssh.FingerprintSHA256(jump.HostKey()),
		"It matches, continue", "Stop", `value="Parents"`, `name="test" value="`+strconv.FormatInt(id, 10)+`"`)
	if strings.Contains(page, `hx-trigger="every 1s"`) {
		t.Error("a test waiting for the admin doesn't poll")
	}
	confirm := func(id int64) int64 {
		loc := location(t, h.Login.Post("/routing/routers/tests/"+strconv.FormatInt(id, 10)+"/confirm", url.Values{"decision": {"trust"}, "name": {"Parents"}, "list": {"1"}}))
		h.Settle()
		return testIDOf(t, loc)
	}
	id = confirm(id)
	has(t, "router fingerprint", h.Login.Get("/routing/routers/new?test="+strconv.FormatInt(id, 10)).Body.String(),
		"First contact with the router "+c.Address(), ssh.FingerprintSHA256(r.Server.HostKey()))
	id = confirm(id)
	page = h.Login.Get("/routing/routers/new?test=" + strconv.FormatInt(id, 10) + "&name=Parents&list=1").Body.String()
	has(t, "checks", page, "passed", "RouterOS version and board", "7.24.5 (stable) · RB5009UG+S+", "DoH forwarder vpn-doh and mtvpn:doh pin",
		"Entries in to_vpn_list", "File upload (SFTP)", `value="`+c.Host+`"`)

	// Save with the test: connected, the initial sync runs
	f := connForm(c, "Parents", 1)
	f.Set("test", strconv.FormatInt(id, 10))
	loc = location(t, h.Login.Post("/routing/routers", f))
	if !strings.HasPrefix(loc, "/routing/routers/") || !strings.HasSuffix(loc, "?added=1") {
		t.Fatalf("to the router: %s", loc)
	}
	h.Settle()
	page = h.Login.Get(loc).Body.String()
	has(t, "saved router", page, "<h1>Parents</h1>", "Router added: its initial sync runs now.", "synced")
	if !r.Holds("openai.com") {
		t.Error("the initial sync ran")
	}

	// Save without a test: the router waits
	other := routerostest.New(t, k)
	loc = location(t, h.Login.Post("/routing/routers", connForm(routingtest.Conn(other, nil), "Untested", 1)))
	page = h.Login.Get(loc).Body.String()
	has(t, "untested", page, "Not connected yet: Test connection to confirm the host keys; the initial sync runs after it passes.", "not connected yet")
	// an invalid form comes back with its errors
	bad := connForm(c, "", 1)
	bad.Set("host", "http://x")
	rec := h.Login.Post("/routing/routers", bad)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid: %d", rec.Code)
	}
	has(t, "errors", rec.Body.String(), "Enter a name of 1 to 60 characters.", "Enter a hostname or an IP address, without a scheme or a port.")
}

func TestRouterPage(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Home", 0, routingtest.ViaJump())
	r.Seed("netflix", entries("netflix.com")...)
	h.StartJobs()
	path := "/routing/routers/" + strconv.FormatInt(id, 10)

	// Preview sync: polls while it runs, then the plan and the script
	loc := location(t, h.Login.Post(path+"/preview", url.Values{}))
	if loc != path+"#preview" {
		t.Fatalf("preview: %s", loc)
	}
	h.Settle()
	page := h.Login.Get(path).Body.String()
	has(t, "router page", page, "<h1>Home</h1>", "never synced", "active · follows Main (1 service, 2 domains) · RouterOS 7.24.5 · RB5009UG+S+",
		`data-key="s"`, "Sync now", "Preview sync",
		"Sync preview", "read from the router at", "nothing applied", "1 update · 0 unchanged · 1 unmanaged, left alone",
		"order: tags that gain names, other updates, then removals", ">openai<", "v2fly:openai", "new", ">netflix<", "unmanaged", "Proxier never installed it",
		"# proxier sync · router Home · preview · file 1/1: update openai (+2)", `data-copy-from="#preview-script"`, "/script.rsc", "Run this sync",
		"Connection", "proxier@"+r.Addr, "pi@", "SHA256:", "direct", "to_vpn_list", "vpn-doh", "Edit connection…",
		"Sync history", "preview", "Actions", "Routing list…", "Activity")
	checkNoInline(t, "router page", page)
	// the DOM order of the areas
	order := []string{`aria-label="Sync preview"`, `aria-label="Tags"`, `aria-label="Connection"`, `aria-label="Sync history"`, `aria-label="Actions"`, `aria-label="Activity"`}
	last := -1
	for _, s := range order {
		i := strings.Index(page, s)
		if i < 0 || i < last {
			t.Errorf("%s out of order", s)
		}
		last = i
	}
	script := h.Login.Get(path + "/syncs/1/script.rsc")
	if script.Code != 200 || !strings.Contains(script.Body.String(), "/ip dns static add name=openai.com") ||
		!strings.Contains(script.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("script: %d %s", script.Code, script.Header())
	}

	// Sync now, then the history, the tags and the status
	loc = location(t, h.Login.Post(path+"/sync", url.Values{}))
	if loc != path+"?synced=1" {
		t.Fatalf("sync: %s", loc)
	}
	h.Settle()
	page = h.Login.Get(path).Body.String()
	has(t, "after a sync", page, "synced", "1 tag installed", "Sync now", "the last plan · sync", `href="/jobs/`, `href="`+path+`/syncs/2"`,
		"Synced router Home")
	plan := h.Login.Get(path + "/syncs/2").Body.String()
	has(t, "plan page", plan, "sync #2", ">openai<", "update", "1 tag installed")

	// a failure shows the band with Test connection
	r.Offline(true)
	h.Login.Post(path+"/sync", url.Values{})
	h.Settle()
	page = h.Login.Get(path).Body.String()
	has(t, "failed", page, "Sync failed · 1 in a row", "Proxier can&#39;t reach the router", "Nothing was changed on the router.", "next retry at",
		`name="router" value="`+strconv.FormatInt(id, 10)+`"`)
	if strings.Index(page, "Sync failed · 1 in a row") > strings.Index(page, ">Tags<") {
		t.Error("the band comes first")
	}

	// edit and routing list pages
	edit := h.Login.Get(path + "/edit").Body.String()
	has(t, "edit", edit, "Edit Home", `value="Home"`, "leaves the entries under the old names on the router")
	parents := h.List("Parents")
	if loc := location(t, h.Login.Post(path+"/list", url.Values{"list": {strconv.FormatInt(parents, 10)}})); loc != path+"?saved=1" {
		t.Fatalf("list: %s", loc)
	}
	has(t, "list switched", h.Login.Get(path).Body.String(), "follows Parents", "Home follows Parents instead of Main")
	f := url.Values{"name": {"Home 2"}, "host": {"10.230.1.1"}, "port": {"22"}, "user": {"proxier"}, "first_hop": {"direct"}, "list_name": {"to_vpn_list"}, "forwarder": {"vpn-doh"}}
	if loc := location(t, h.Login.Post(path+"/edit", f)); loc != path+"?saved=1" {
		t.Fatalf("edit: %s", loc)
	}
	has(t, "edited", h.Login.Get(path).Body.String(), "<h1>Home 2</h1>", "Renamed router Home to Home 2")

	// the phone layout: the band and the actions come before the areas, in
	// the columns app.css stacks into one under 760 px
	if !strings.Contains(page, `class="subcols"`) || strings.Index(page, `data-action="routers.sync_now"`) > strings.Index(page, `class="subcols"`) {
		t.Error("the page uses the stacking columns, actions first")
	}
}

func TestRoutersListAndTargets(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	_, home := h.Router("Home", 0)
	_, parents := h.Router("Parents", 0, routingtest.ViaJump())
	h.StartJobs()
	if _, err := h.Mod.Routers.SyncNow(bg(), home, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()

	list := h.Login.Get("/routing/routers").Body.String()
	has(t, "routers", list, "<h1>Routers</h1>", `href="/routing/routers/new"`, `data-key="n"`, "Add router",
		"Router", "State", "List", "Last sync",
		">Home<", ">Parents<", "RB5009UG+S+ · 7.24.5", "via jump host 127.0.0.1", "active", ">Main<",
		"synced", "1 tag installed", "never synced", `action="/routing/routers/`+strconv.FormatInt(home, 10)+`/sync"`, `data-row-only`)
	checkNoInline(t, "routers", list)

	// a change: the list page names the routers with their words
	h.Up.V2fly("anthropic", "anthropic.com\n")
	h.List("Main", h.Upstream("v2fly:anthropic"))
	lp := h.Login.Get("/routing/lists/1").Body.String()
	has(t, "list targets", lp, `href="/routing/routers/`+strconv.FormatInt(home, 10)+`"`, "router · waits 30 s", ">Parents<")
	lists := h.Login.Get("/routing/lists").Body.String()
	has(t, "lists page", lists, ">Home<", "router · waits 30 s")
	_ = parents

	// the custom editor names the routers that re-sync
	mine := h.Custom("mine", "mine.org")
	h.List("Main", mine)
	has(t, "editor footer", h.Login.Get("/routing/services/"+strconv.FormatInt(mine, 10)+"/edit").Body.String(),
		"Saving re-syncs Home and Parents, the routers of Main.")
}

func entries(names ...string) []routeros.Entry {
	out := make([]routeros.Entry, 0, len(names))
	for _, n := range names {
		out = append(out, routeros.Entry{Name: n})
	}
	return out
}

func names(prefix string, n int) []routeros.Entry {
	out := make([]routeros.Entry, n)
	for i := range out {
		out[i] = routeros.Entry{Name: prefix + "-" + strconv.Itoa(i) + ".com"}
	}
	return out
}

func customDomains(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + "-" + strconv.Itoa(i) + ".com"
	}
	return out
}

func TestRouterLifecyclePages(t *testing.T) {
	h := routingtest.New(t)
	h.List("Main", h.Custom("youtube", customDomains("yt", 30)...))
	if err := h.App.Settings.Set(bg(), "admin", "routing", map[string]string{"routing.drift_repair": "false"}); err != nil {
		t.Fatal(err)
	}
	failing, home := h.Router("Home", 0)
	drifty, drift := h.Router("Drifty", 0)
	unm, unmID := h.Router("Unm", 0)
	_, paused := h.Router("Quiet", 0)
	_, _, awaiting := h.Registered("Waiting")
	unm.Seed("old-work", entries("work.example", "old.example")...)
	unm.SeedUntagged("hand.example", false)
	h.StartJobs()
	for _, id := range []int64{home, drift, unmID, paused} {
		if _, err := h.Mod.Routers.SyncNow(bg(), id, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	h.Settle()
	drifty.DeleteEntries("youtube", 10)
	h.RunSchedule(routers.JobDriftRound)
	h.Settle()
	failing.Offline(true)
	h.Login.Post("/routing/routers/"+strconv.FormatInt(home, 10)+"/sync", url.Values{})
	h.Settle()
	if loc := location(t, h.Login.Post("/routing/routers/"+strconv.FormatInt(paused, 10)+"/pause", url.Values{})); loc != "/routing/routers/"+strconv.FormatInt(paused, 10) {
		t.Fatalf("pause: %s", loc)
	}

	// the list: states, watch and order
	list := h.Login.Get("/routing/routers").Body.String()
	has(t, "routers", list, "Watch", "▲ drift: youtube", "▲ 1 unmanaged tag: old-work", ">none<", "paused",
		"never connected · trying every 10 min until")
	order := []string{">Home<", ">Drifty<", ">Unm<", ">Quiet<", ">Waiting<"}
	for i := 1; i < len(order); i++ {
		if strings.Index(list, order[i-1]) > strings.Index(list, order[i]) {
			t.Errorf("%s before %s", order[i-1], order[i])
		}
	}
	checkNoInline(t, "routers", list)

	// drift: the band with Repair, the connection's drift line
	path := "/routing/routers/" + strconv.FormatInt(drift, 10)
	page := h.Login.Get(path).Body.String()
	has(t, "drift", page, "Drift found at", "youtube has 10 entries fewer than Proxier installed", `action="`+path+`/repair"`,
		"Drift check", "auto-repair off", "Pause syncs", `href="`+path+`/remove"`)
	if loc := location(t, h.Login.Post(path+"/repair", url.Values{})); loc != path+"?repairing=1" {
		t.Fatalf("repair: %s", loc)
	}
	h.Settle()
	if strings.Contains(h.Login.Get(path).Body.String(), "Drift found at") {
		t.Error("repaired: the band goes")
	}

	// unmanaged tags, untagged entries and infra pins
	path = "/routing/routers/" + strconv.FormatInt(unmID, 10)
	page = h.Login.Get(path).Body.String()
	has(t, "unmanaged", page, "Not Proxier&#39;s", "found on the router · never deleted on its own", ">old-work<", "2 entries · seen since",
		"Adopt…", `href="`+path+`/unmanaged/old-work/adopt"`, "Ignore", "Remove from router…", "Remove old-work (2 entries) from Unm?",
		"entries without a comment", "commented mtvpn: belong to the router script. Never touched.")
	location(t, h.Login.Post(path+"/unmanaged/old-work/ignore", url.Values{}))
	page = h.Login.Get(path).Body.String()
	has(t, "ignored", page, "Ignored · 1", "Stop ignoring")
	if strings.Contains(h.Login.Get("/routing/routers").Body.String(), "unmanaged tag: old-work") {
		t.Error("an ignored tag leaves the watch")
	}

	// a failure's band with its hops
	page = h.Login.Get("/routing/routers/" + strconv.FormatInt(home, 10)).Body.String()
	has(t, "hops", page, "Per hop", "router 127.0.0.1", "offline")

	// awaiting setup, paused
	page = h.Login.Get("/routing/routers/" + strconv.FormatInt(awaiting, 10)).Body.String()
	key, _, _ := h.App.SSH.PublicKey(bg())
	has(t, "awaiting", page, "Awaiting setup: Proxier tries to connect every 10 minutes until", html.EscapeString(strings.TrimSpace(key)),
		`data-action="routers.test_saved"`)
	if strings.Contains(page, `data-action="routers.sync_now"`) {
		t.Error("no Sync now while awaiting")
	}
	path = "/routing/routers/" + strconv.FormatInt(paused, 10)
	page = h.Login.Get(path).Body.String()
	has(t, "paused", page, "Paused: changes wait until you resume.", `action="`+path+`/resume"`, "Resume")
	if loc := location(t, h.Login.Post(path+"/resume", url.Values{})); loc != path+"?synced=1" {
		t.Fatalf("resume: %s", loc)
	}
	h.Settle()

	// the phone width: the bands and the actions come before the areas
	page = h.Login.Get("/routing/routers/" + strconv.FormatInt(home, 10)).Body.String()
	cols := strings.Index(page, `class="subcols"`)
	for _, s := range []string{`data-action="routers.sync_now"`, `data-action="routers.pause"`, "Per hop", `data-action="routers.test_saved"`} {
		if i := strings.Index(page, s); i < 0 || i > cols {
			t.Errorf("%s comes first", s)
		}
	}
}

func TestAdoptAndRemovePages(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", strings.Join(customDomains("nf", 26), "\n")+"\n")
	h.StartJobs()
	if _, err := h.Mod.Catalog.RefreshNow(bg(), "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	h.List("Main", h.Custom("mine", "example.org"))
	r, id := h.Router("Home", 0)
	r.Seed("netflix", names("nf", 26)...)
	r.Seed("my stuff", entries("stuff.example")...)
	if _, err := h.Mod.Routers.SyncNow(bg(), id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	path := "/routing/routers/" + strconv.FormatInt(id, 10)

	adopt := h.Login.Get(path + "/unmanaged/netflix/adopt").Body.String()
	has(t, "adopt", adopt, "<h1>Adopt netflix</h1>", "netflix has 26 entries on Home that Proxier never installed", "Adopt as v2fly:netflix",
		`value="v2fly"`, "Make a custom service from the router&#39;s 26 entries", `value="custom"`, "Remove from router…", "Ignore")
	checkNoInline(t, "adopt", adopt)

	bad := h.Login.Get(path + "/unmanaged/my%20stuff/adopt")
	if bad.Code != 200 {
		t.Fatalf("adopt my stuff: %d", bad.Code)
	}
	has(t, "invalid", bad.Body.String(), "can&#39;t be a service&#39;s tag", "Remove from router…", "Ignore")
	if strings.Contains(bad.Body.String(), `name="how"`) {
		t.Error("an invalid tag offers no adoption")
	}
	if rec := h.Login.Post(path+"/unmanaged/my%20stuff/adopt", url.Values{"how": {"custom"}}); rec.Code != 409 {
		t.Fatalf("adopt invalid: %d", rec.Code)
	}
	if rec := h.Login.Get(path + "/unmanaged/nothing/adopt"); rec.Code != 404 {
		t.Fatalf("no such tag: %d", rec.Code)
	}

	if loc := location(t, h.Login.Post(path+"/unmanaged/netflix/adopt", url.Values{"how": {"v2fly"}})); loc != path+"?adopted=netflix" {
		t.Fatalf("adopted: %s", loc)
	}
	h.Settle()
	has(t, "after adopting", h.Login.Get(path+"?adopted=netflix").Body.String(), "Adopted netflix: a sync runs now and records it.")

	// remove: keep is the default; cleaning names what goes
	rm := h.Login.Get(path + "/remove").Body.String()
	has(t, "remove", rm, "<h1>Remove Home</h1>", "Keep everything on the router", `name="clean" value="0" checked`,
		"Remove every tag Proxier installed", "deletes the 2 tags Proxier installed")
	checkNoInline(t, "remove", rm)
	_, _, awaiting := h.Registered("Waiting")
	has(t, "remove awaiting", h.Login.Get("/routing/routers/"+strconv.FormatInt(awaiting, 10)+"/remove").Body.String(), `name="clean" value="1" disabled`)

	if loc := location(t, h.Login.Post(path+"/remove", url.Values{"clean": {"0"}})); loc != "/routing/routers?removed=Home" {
		t.Fatalf("removed: %s", loc)
	}
	has(t, "list", h.Login.Get("/routing/routers?removed=Home").Body.String(), "Removed Home.")
	if rec := h.Login.Get(path); rec.Code != 404 {
		t.Fatalf("gone: %d", rec.Code)
	}
}

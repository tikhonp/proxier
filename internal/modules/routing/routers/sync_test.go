package routers_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestFirstSyncOfMtvpnRouterPushesNothing(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\nfull:api.openai.com\n")
	openai := h.Upstream("v2fly:openai")
	mine := h.Custom("mine", "example.org", "full:x.example.net")
	h.List("Main", openai, mine)
	r, id := h.Router("Home", 0)
	// as mtvpn installed them
	r.Seed("openai", entries("openai.com", "chatgpt.com", "full:api.openai.com")...)
	r.Seed("mine", entries("example.org", "full:x.example.net")...)

	syncNow(t, h, id)
	if imp := r.Imports(); len(imp) != 0 || len(r.Files()) != 0 {
		t.Fatalf("nothing is pushed: imports %v, files %v", imp, r.Files())
	}
	s := lastSync(t, h, id)
	if s.State != "done" || s.Recorded != 2 || s.Pushed() != 0 {
		t.Fatalf("sync: %+v", s)
	}
	a := applied(t, h, id)
	if a["openai"].Hash != routeros.EntriesHash(entries("openai.com", "chatgpt.com", "full:api.openai.com")) || a["openai"].Suffix != 2 || a["openai"].Exact != 1 || len(a) != 2 {
		t.Fatalf("applied: %+v", a)
	}
	ev := h.Events("routing.router_synced")
	if len(ev) != 1 || num(ev[0].Payload["recorded"]) != 2 || num(ev[0].Payload["added"]) != 0 || ev[0].Payload["trigger"] != "manual" {
		t.Fatalf("synced: %+v", ev)
	}
	if rt := router(t, h, id); rt.LastResult != "synced" || rt.InfraPins != 2 || rt.Failures != 0 {
		t.Fatalf("router: %+v", rt)
	}
	// a second sync has nothing to do and records no event
	syncNow(t, h, id)
	if s := lastSync(t, h, id); s.Unchanged != 2 || s.Recorded != 0 || len(h.Events("routing.router_synced")) != 1 {
		t.Fatalf("second sync: %+v", s)
	}
}

func TestAddServiceSyncsOnce(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	r, id := h.Router("Home", 0)
	h.StartJobs()
	h.List("Main", h.Upstream("v2fly:openai"))
	jobs := h.JobsOf(routers.JobSync)
	if len(jobs) != 1 || !jobs[0].RunAfter.Equal(h.Clock().Add(30*time.Second)) {
		t.Fatalf("one sync, 30 s later: %+v", jobs)
	}
	h.Settle()
	if len(r.Imports()) != 0 {
		t.Fatal("the sync waits its 30 s")
	}
	h.Advance(31 * time.Second)
	h.Settle()
	if imp := r.Imports(); len(imp) != 1 || !strings.Contains(string(r.Import(1)), "# update openai (+2)\n") {
		t.Fatalf("one update file for openai: %v", imp)
	}
	if got := r.Names("openai"); !slices.Equal(got, []string{"chatgpt.com", "openai.com"}) {
		t.Fatalf("router: %v", got)
	}
	if len(h.JobsOf(routers.JobSync)) != 1 || len(r.Files()) != 0 {
		t.Fatalf("jobs %d, files %v", len(h.JobsOf(routers.JobSync)), r.Files())
	}
	s := lastSync(t, h, id)
	if s.Added != 1 || s.Trigger != "change" || s.State != "done" {
		t.Fatalf("sync: %+v", s)
	}
}

func TestChangesCoalesce(t *testing.T) {
	h := routingtest.New(t)
	r, _ := h.Router("Home", 0)
	h.StartJobs()
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		h.Up.V2fly(name, name+".com\n")
		h.List("Main", h.Upstream("v2fly:"+name))
		if i < 4 {
			h.Advance(10 * time.Second)
			h.Settle()
		}
	}
	jobs := h.JobsOf(routers.JobSync)
	if len(jobs) != 1 || jobs[0].Merged != 4 || !jobs[0].RunAfter.Equal(h.Clock().Add(30*time.Second)) {
		t.Fatalf("one coalesced sync, 30 s after the last change: %+v", jobs)
	}
	if len(r.Imports()) != 0 {
		t.Fatal("nothing runs before")
	}
	h.Advance(31 * time.Second)
	h.Settle()
	if len(r.Imports()) != 1 {
		t.Fatalf("imports: %v", r.Imports())
	}
	ops, err := routeros.ParseScript(r.Import(1))
	if err != nil || len(ops) != 5 {
		t.Fatalf("five update blocks: %+v %v", ops, err)
	}
	if got := r.Tags(); !slices.Equal(got, []string{"a", "b", "c", "d", "e"}) {
		t.Fatalf("tags: %v", got)
	}
	if len(h.JobsOf(routers.JobSync)) != 1 {
		t.Fatal("one sync")
	}
}

func TestTwoRefreshesOneSync(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("a", "a.com\n")
	h.Up.V2fly("b", "b.com\n")
	a, b := h.Upstream("v2fly:a"), h.Upstream("v2fly:b")
	h.List("Main", a, b)
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	before := len(h.JobsOf(routers.JobSync))
	h.Up.V2fly("a", "a.com\na2.com\n")
	h.Up.V2fly("b", "b.com\nb2.com\n")
	for _, sid := range []int64{a, b} {
		if out, err := h.Mod.Refresh.Refresh(bg, sid, false, "admin"); err != nil {
			t.Fatalf("refresh: %v %+v", err, out)
		}
	}
	if n := len(h.JobsOf(routers.JobSync)); n != before+1 {
		t.Fatalf("one sync for both refreshes: %d", n-before)
	}
	h.Advance(31 * time.Second)
	h.Settle()
	if !slices.Equal(r.Names("a"), []string{"a.com", "a2.com"}) || !slices.Equal(r.Names("b"), []string{"b.com", "b2.com"}) {
		t.Fatalf("router: %v %v", r.Names("a"), r.Names("b"))
	}
	if s := lastSync(t, h, id); s.Updated != 2 || len(r.Imports()) != 2 {
		t.Fatalf("sync: %+v, imports %v", s, r.Imports())
	}
}

func TestRemovedServiceIsRemoved(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	openai := h.Upstream("v2fly:openai")
	main := h.List("Main", openai)
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	if !slices.Equal(r.Tags(), []string{"openai"}) {
		t.Fatalf("installed: %v", r.Tags())
	}
	if err := h.Mod.Lists.Remove(bg, main, openai, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Advance(31 * time.Second)
	h.Settle()
	if len(r.Tags()) != 0 || r.Holds("openai.com") {
		t.Fatalf("removed: %v", r.Tags())
	}
	if _, ok := applied(t, h, id)["openai"]; ok {
		t.Fatal("its applied state is forgotten")
	}
	s := lastSync(t, h, id)
	if s.Removed != 1 || s.Plan[0].Action != routers.Remove || s.Plan[0].Why != routers.WhyLeftList {
		t.Fatalf("sync: %+v", s)
	}
	if !strings.Contains(string(r.Import(2)), `/ip dns static remove [find comment="openai" address-list="to_vpn_list"]`) {
		t.Fatalf("removal block: %s", r.Import(2))
	}
}

// claudeSetup: anthropic (anthropic.com, claude.ai) and mine (claude.ai,
// mine.org) in Main, anthropic first, so it owns claude.ai; synced.
func claudeSetup(t *testing.T) (*routingtest.Harness, int64, int64, int64, int64) {
	t.Helper()
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	anthropic := h.Upstream("v2fly:anthropic")
	mine := h.Custom("mine", "claude.ai", "mine.org")
	main := h.List("Main", anthropic, mine)
	_, id := h.Router("Home", 0)
	return h, main, anthropic, mine, id
}

func TestGainingTagPushedFirst(t *testing.T) {
	h, main, _, mine, id := claudeSetup(t)
	r := h.RouterFake(id)
	syncNow(t, h, id)
	if !slices.Equal(r.Names("anthropic"), []string{"anthropic.com", "claude.ai"}) || !slices.Equal(r.Names("mine"), []string{"mine.org"}) {
		t.Fatalf("first: %v %v", r.Names("anthropic"), r.Names("mine"))
	}
	r.OnImport(func(int) {
		if !r.Holds("claude.ai") {
			t.Error("claude.ai missing after an import")
		}
	})
	if _, _, err := h.Mod.Lists.Move(bg, main, mine, false, "admin"); err != nil { // mine up
		t.Fatal(err)
	}
	h.Advance(31 * time.Second)
	h.Settle()
	body := string(r.Import(2))
	if i, j := strings.Index(body, "# update mine"), strings.Index(body, "# update anthropic"); i < 0 || j < 0 || i > j {
		t.Fatalf("mine first:\n%s", body)
	}
	if !slices.Equal(r.Names("mine"), []string{"claude.ai", "mine.org"}) || !slices.Equal(r.Names("anthropic"), []string{"anthropic.com"}) {
		t.Fatalf("after: %v %v", r.Names("mine"), r.Names("anthropic"))
	}
	if p := lastSync(t, h, id).Plan; p[0].Tag != "mine" || !p[0].Gains || p[1].Tag != "anthropic" || p[1].Gains {
		t.Fatalf("plan: %+v", p)
	}
}

func TestNameNeverLostWhenOwnerLeaves(t *testing.T) {
	h, main, anthropic, _, id := claudeSetup(t)
	r := h.RouterFake(id)
	syncNow(t, h, id)
	r.OnImport(func(int) {
		if !r.Holds("claude.ai") {
			t.Error("claude.ai missing after an import")
		}
	})
	if err := h.Mod.Lists.Remove(bg, main, anthropic, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Advance(31 * time.Second)
	h.Settle()
	if !slices.Equal(r.Tags(), []string{"mine"}) || !slices.Equal(r.Names("mine"), []string{"claude.ai", "mine.org"}) {
		t.Fatalf("router: %v %v", r.Tags(), r.Names("mine"))
	}
	body := string(r.Import(2))
	if i, j := strings.Index(body, "# update mine"), strings.Index(body, "# remove anthropic"); i < 0 || j < 0 || i > j {
		t.Fatalf("the update before the removal:\n%s", body)
	}
}

func TestRenamedTagReplaced(t *testing.T) {
	h := routingtest.New(t)
	mine := h.Custom("mine", "mine.org")
	h.List("Main", mine)
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	setCustom(t, h, mine, "personal", "mine.org")
	h.Advance(31 * time.Second)
	h.Settle()
	if !slices.Equal(r.Tags(), []string{"personal"}) || !slices.Equal(r.Names("personal"), []string{"mine.org"}) {
		t.Fatalf("router: %v", r.Tags())
	}
	body := string(r.Import(2))
	if i, j := strings.Index(body, "# update personal"), strings.Index(body, "# remove mine"); i < 0 || j < 0 || i > j {
		t.Fatalf("install then remove:\n%s", body)
	}
	a := applied(t, h, id)
	if _, ok := a["mine"]; ok || a["personal"].Hash == "" {
		t.Fatalf("applied: %+v", a)
	}
}

func TestUntaggedDuplicateAdopted(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\nchatgpt.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Home", 0)
	r.SeedUntagged("chatgpt.com", false)
	syncNow(t, h, id)
	if r.Untagged() != 0 || !slices.Equal(r.Names("openai"), []string{"chatgpt.com", "openai.com"}) {
		t.Fatalf("untagged %d, openai %v", r.Untagged(), r.Names("openai"))
	}
	if _, list := r.Tag("openai"); len(list) != 2 {
		t.Fatalf("address list: %+v", list)
	}
}

func TestUntaggedLeftAlone(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Home", 0)
	r.SeedUntagged("example.org", false)
	syncNow(t, h, id)
	if r.Untagged() != 2 || !r.Holds("example.org") {
		t.Fatalf("left alone: %d", r.Untagged())
	}
	if rt := router(t, h, id); rt.Untagged != 2 {
		t.Fatalf("counted: %+v", rt)
	}
	if p := lastSync(t, h, id).Plan; len(p) != 1 {
		t.Fatalf("no row for untagged entries: %+v", p)
	}
}

func TestInfraPinNeverInstalled(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("google", "google.com\ndns.google\n")
	h.List("Main", h.Upstream("v2fly:google"))
	r, id := h.Router("Home", 0)
	job := syncNow(t, h, id)
	if !slices.Equal(r.Names("google"), []string{"google.com"}) {
		t.Fatalf("installed: %v", r.Names("google"))
	}
	if log := jobLog(t, h, job); !strings.Contains(log, "dns.google: skipped, it is an infra pin (mtvpn:doh) on Home.") {
		t.Fatalf("log:\n%s", log)
	}
	if lastSync(t, h, id).State != "done" {
		t.Fatal("the sync verifies without the pin")
	}
}

func TestOfflineRetriesAndRecovers(t *testing.T) {
	h := routingtest.New(t)
	r, id := h.Router("Home", 0)
	r.Offline(true)
	h.StartJobs()
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	h.Advance(31 * time.Second)
	h.Settle()
	for i, wait := range []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour} {
		ev := h.Events("routing.router_sync_failed")
		if len(ev) != i+1 {
			t.Fatalf("attempt %d: %d failures", i+1, len(ev))
		}
		e := ev[i]
		if e.Payload["step"] != "connect" || num(e.Payload["consecutive"]) != i+1 || truthy(e.Payload["final"]) || notifies(t, e) {
			t.Fatalf("attempt %d: %+v", i+1, e.Payload)
		}
		h.Advance(wait - time.Second)
		h.Settle()
		if len(h.Events("routing.router_sync_failed")) != i+1 {
			t.Fatalf("attempt %d came before its backoff", i+2)
		}
		h.Advance(time.Second)
		h.Settle()
	}
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 4 || !truthy(ev[3].Payload["final"]) || !notifies(t, ev[3]) || num(ev[3].Payload["attempt"]) != 4 {
		t.Fatalf("the fourth gives up: %+v", ev)
	}
	if !strings.Contains(ev[3].Payload["error"].(string), "Proxier can't reach the router 127.0.0.1:") ||
		!strings.HasSuffix(ev[3].Payload["error"].(string), "Nothing was changed on the router.") {
		t.Fatalf("problem: %v", ev[3].Payload["error"])
	}
	if len(h.Events("job.failed")) != 0 {
		t.Fatal("no job.failed besides the module's own event")
	}
	rt := router(t, h, id)
	if rt.Failures != 4 || !rt.FailureNotified || rt.LastResult != "failed" {
		t.Fatalf("router: %+v", rt)
	}
	if s := lastSync(t, h, id); s.State != "failed" || s.Step != "connect" || s.Hop != "router" {
		t.Fatalf("row: %+v", s)
	}
	r.Offline(false)
	syncNow(t, h, id)
	rec := h.Events("routing.router_recovered")
	if len(rec) != 1 || !truthy(rec[0].Payload["notified"]) || num(rec[0].Payload["failures"]) != 4 || !notifies(t, rec[0]) {
		t.Fatalf("recovered: %+v", rec)
	}
	if rt := router(t, h, id); rt.Failures != 0 || rt.FailureNotified || rt.LastResult != "synced" {
		t.Fatalf("router: %+v", rt)
	}

	// failing once and then succeeding recovers quietly
	r.Offline(true)
	syncNow(t, h, id)
	r.Offline(false)
	h.Advance(5 * time.Minute)
	h.Settle()
	rec = h.Events("routing.router_recovered")
	if len(rec) != 2 || truthy(rec[1].Payload["notified"]) || notifies(t, rec[1]) {
		t.Fatalf("quiet recovery: %+v", rec)
	}
}

func TestSyncNowRunsAtOnce(t *testing.T) {
	h := routingtest.New(t)
	r, id := h.Router("Home", 0)
	h.StartJobs()
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	if _, err := h.Mod.Routers.SyncNow(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Settle()
	jobs := h.JobsOf(routers.JobSync)
	if len(jobs) != 1 || string(jobs[0].State) != "succeeded" || len(r.Imports()) != 1 {
		t.Fatalf("merged and run at once: %+v, imports %v", jobs, r.Imports())
	}
	if s := lastSync(t, h, id); s.Trigger != "manual" {
		t.Fatalf("trigger: %+v", s)
	}

	// a refused key is final at its first attempt and notifies
	r2, id2 := h.Router("Parents", 0)
	r2.Server.BlockLogins("*", true)
	syncNow(t, h, id2)
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || !truthy(ev[0].Payload["final"]) || num(ev[0].Payload["attempt"]) != 1 || !truthy(ev[0].Payload["manual"]) || !notifies(t, ev[0]) {
		t.Fatalf("final at once: %+v", ev)
	}
	if !strings.Contains(ev[0].Payload["error"].(string), "refused Proxier's key: install it on the router.") {
		t.Fatalf("problem: %v", ev[0].Payload["error"])
	}
	if s := lastSync(t, h, id2); s.Hop != "router" || s.Step != "connect" {
		t.Fatalf("row: %+v", s)
	}
}

func TestRestartDuringImport(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("big1", lines("one", 1500))
	h.Up.V2fly("big2", lines("two", 1000))
	h.List("Main", h.Upstream("v2fly:big1"), h.Upstream("v2fly:big2"))
	r, id := h.Router("Home", 0)
	release := r.Hold(2)
	h.StartJobs()
	job, err := h.Mod.Routers.SyncNow(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the second import", func() bool { return len(r.Imports()) == 2 })
	h.StopJobs()
	if st, _ := h.Job(job); st != "interrupted" {
		t.Fatalf("job: %s", st)
	}
	if len(r.Names("big1")) != 1500 || len(r.Names("big2")) != 0 {
		t.Fatal("the first file landed, the second didn't")
	}
	h.StartJobs()
	h.Settle()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("resumed job: %s %s", st, e)
	}
	imp := r.Imports()
	if len(imp) != 3 || imp[2] != routeros.FileName(itoa(job), 1) {
		t.Fatalf("imports: %v", imp)
	}
	ops, _ := routeros.ParseScript(r.Import(3))
	if len(ops) != 1 || ops[0].Tag != "big2" {
		t.Fatalf("re-planned: only big2 again: %+v", ops)
	}
	if len(r.Files()) != 0 {
		t.Fatalf("the leftover file is removed: %v", r.Files())
	}
	if len(r.Names("big2")) != 1000 || len(r.Names("big1")) != 1500 {
		t.Fatal("both tags end correct")
	}
	release()
	syncs, _ := h.Mod.Routers.Syncs(bg, id, 0, 10)
	if len(syncs) != 1 || syncs[0].State != "done" {
		t.Fatalf("one row, reused: %+v", syncs)
	}
}

func TestChangedHostKeyStopsSync(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Home", 0)
	r.Server.RotateHostKey()
	job := syncNow(t, h, id)
	if st, _ := h.Job(job); st != "failed" {
		t.Fatalf("job: %s", st)
	}
	if len(r.Imports()) != 0 || len(r.Files()) != 0 {
		t.Fatal("nothing uploaded")
	}
	if len(h.Events("ssh.host_key_changed")) != 1 {
		t.Fatal("the security event")
	}
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || !truthy(ev[0].Payload["final"]) || !strings.Contains(ev[0].Payload["error"].(string), "host key changed") {
		t.Fatalf("failed: %+v", ev)
	}
}

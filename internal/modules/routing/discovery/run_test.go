package discovery_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery/discoverytest"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestCatalogSuggestionsFirst(t *testing.T) {
	h, b := newH(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	refreshCatalog(t, h)
	release := b.Block()
	defer release()
	id := start(t, h, discovery.Start{Website: "claude.ai", Via: discovery.ViaDirect})
	if !b.Entered(10 * time.Second) {
		t.Fatal("the visit never started")
	}
	// the visit is still held: the suggestions are already there
	r, err := h.Mod.Discovery.Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != discovery.Running || len(r.Suggestions) != 1 || r.Suggestions[0].Selector != "v2fly:anthropic" ||
		r.Suggestions[0].Match != "suffix" || r.Suggestions[0].Name != "claude.ai" || r.Suggestions[0].Domains != 2 {
		t.Fatalf("while visiting: %s %+v", r.State, r.Suggestions)
	}
	if len(r.Visits) != 0 {
		t.Errorf("visits before the visit ended: %+v", r.Visits)
	}
	release()
	r = wait(t, h, id, discovery.Done)
	if len(r.Suggestions) != 1 {
		t.Errorf("suggestions after: %+v", r.Suggestions)
	}
	if ev := h.Events("routing.discovery_completed"); len(ev) != 1 || ev[0].Payload["website"] != "claude.ai" ||
		ev[0].Payload["suggestions"] != float64(1) || ev[0].Subject.String() != fmt.Sprintf("discovery:%d", id) {
		t.Errorf("completed: %+v", ev)
	}
}

// blocked is a site whose every request is reset from home.
func blocked(url string) discovery.Result {
	return discovery.Result{
		Pages: []discovery.Page{{URL: url, Error: "net::ERR_CONNECTION_RESET"}},
		Requests: []discovery.Request{
			failed(url, "www.blocked.example", "net::ERR_CONNECTION_RESET"),
			failed("https://static.blockedcdn.net/app.js", "static.blockedcdn.net", "net::ERR_CONNECTION_RESET"),
		},
	}
}

func TestBlockedSiteAndVisitAgain(t *testing.T) {
	h, b := newH(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	url := "https://www.blocked.example/"
	b.Site(url, "", blocked(url))
	b.Site(url, "nl-1", loaded(url, "Blocked at home",
		req(url, "www.blocked.example"), req("https://static.blockedcdn.net/app.js", "static.blockedcdn.net")))

	r := wait(t, h, start(t, h, discovery.Start{Website: "www.blocked.example", Via: discovery.ViaDirect}), discovery.Done)
	if len(r.Visits) != 1 || r.Visits[0].Loaded || r.Visits[0].Error != "net::ERR_CONNECTION_RESET" || r.Visits[0].Failed != 2 {
		t.Fatalf("direct visit: %+v", r.Visits)
	}
	site, _ := hostOf(r, "www.blocked.example")
	cdn, _ := hostOf(r, "static.blockedcdn.net")
	if site.Failed != "net::ERR_CONNECTION_RESET" || site.FailedRequests != 1 || !site.Ticked() {
		t.Errorf("the site's host: %+v", site)
	}
	if cdn.Failed == "" || !cdn.Ticked() || cdn.Class != discovery.ThirdParty {
		t.Errorf("the failed third-party host is ticked: %+v", cdn)
	}
	if g, _ := groupOf(r, "blocked.example"); !g.Ticked || g.Failed != 1 {
		t.Errorf("the site's group: %+v", g)
	}

	again, err := h.Mod.Discovery.Again(bg, r.ID, discovery.ViaServer, 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	r2 := wait(t, h, again, discovery.Done)
	if r2.ID == r.ID || r2.Input != r.Input || r2.Via != discovery.ViaServer || r2.ServerName != "nl-1" || len(r2.Visits) != 1 ||
		!r2.Visits[0].Loaded || r2.Visits[0].Path != "nl-1" || r2.Title != "Blocked at home" {
		t.Fatalf("again through nl-1: %+v", r2)
	}
	// a visit through a server marks nothing failed
	if hst, _ := hostOf(r2, "www.blocked.example"); hst.Failed != "" || !slices.Equal(hst.Seen, []string{"nl-1"}) {
		t.Errorf("through nl-1: %+v", hst)
	}
	if d := h.Servers.Dialed(); !slices.Equal(d, []int64{1}) {
		t.Errorf("dialed %v", d)
	}
	// the first run is untouched
	if r1, _ := h.Mod.Discovery.Get(bg, r.ID); r1.Visits[0].Loaded {
		t.Error("the first run changed")
	}
}

func TestAutoRepeatsThroughServer(t *testing.T) {
	h, b := newH(t)
	h.Servers.Put(1, "nl-1")
	h.Servers.SetHealth(1, "blocked")
	h.Servers.Put(2, "fi-1")
	h.Servers.Put(3, "de-1") // the first healthy one by name
	url := "https://www.blocked.example/"
	b.Site(url, "", blocked(url))
	b.Site(url, "de-1", loaded(url, "Blocked", req(url, "www.blocked.example"), req("https://api.blocked.example/v1", "api.blocked.example")))

	r := wait(t, h, start(t, h, discovery.Start{Website: url, Via: discovery.ViaAuto}), discovery.Done)
	if len(r.Visits) != 2 || r.Visits[0].Path != "direct" || r.Visits[0].Loaded || r.Visits[1].Path != "de-1" || !r.Visits[1].Loaded {
		t.Fatalf("visits: %+v", r.Visits)
	}
	if d := h.Servers.Dialed(); !slices.Equal(d, []int64{3}) {
		t.Errorf("dialed %v", d)
	}
	site, _ := hostOf(r, "www.blocked.example")
	if !slices.Equal(site.Seen, []string{"direct", "de-1"}) || site.Requests != 2 || site.Failed != "net::ERR_CONNECTION_RESET" {
		t.Errorf("merged host: %+v", site)
	}
	if api, ok := hostOf(r, "api.blocked.example"); !ok || api.Failed != "" || !slices.Equal(api.Seen, []string{"de-1"}) {
		t.Errorf("seen only through de-1: %+v", api)
	}
	if r.Title != "Blocked" {
		t.Errorf("title from the loaded visit: %q", r.Title)
	}

	// a direct visit that loads cleanly isn't repeated
	ok := "https://fine.example/"
	b.Site(ok, "", loaded(ok, "Fine", req(ok, "fine.example")))
	r = wait(t, h, start(t, h, discovery.Start{Website: ok, Via: discovery.ViaAuto}), discovery.Done)
	if len(r.Visits) != 1 || len(h.Servers.Dialed()) != 1 {
		t.Errorf("a clean direct visit was repeated: %+v", r.Visits)
	}
}

func TestHostCap(t *testing.T) {
	h, b := newH(t)
	url := "https://news.example/"
	res := loaded(url, "News")
	for i := range 450 {
		host := fmt.Sprintf("h%d.news-cdn%d.net", i, i)
		res.Requests = append(res.Requests, req("https://"+host+"/x", host))
	}
	b.Site(url, "", res)
	r := wait(t, h, start(t, h, discovery.Start{Website: url, Via: discovery.ViaDirect}), discovery.Done)
	if !r.Capped || len(r.Hosts) != discovery.MaxHosts {
		t.Fatalf("capped %v with %d hosts", r.Capped, len(r.Hosts))
	}
	if v := b.Visits(); len(v) != 1 || v[0].MaxHosts != discovery.MaxHosts || v[0].Links != 0 || v[0].Site != "news.example" ||
		v[0].PageTimeout != discovery.PageTimeout {
		t.Errorf("visit: %+v", v)
	}
	rec := h.Login.Get(fmt.Sprintf("/routing/discover/%d", r.ID))
	if !strings.Contains(rec.Body.String(), "Stopped at 300 hostnames.") {
		t.Error("the page doesn't say it stopped")
	}
}

func TestWithoutChromium(t *testing.T) {
	h := routingtest.New(t)
	h.StartJobs()
	h.Up.V2fly("anthropic", "claude.ai\n")
	refreshCatalog(t, h)
	r := wait(t, h, start(t, h, discovery.Start{Website: "claude.ai", Via: discovery.ViaDirect}), discovery.Done)
	if len(r.Visits) != 0 || len(r.Hosts) != 0 || len(r.Suggestions) != 1 {
		t.Fatalf("run: %+v", r)
	}
	body := h.Login.Get(fmt.Sprintf("/routing/discover/%d", r.ID)).Body.String()
	if !strings.Contains(body, "Chromium is not configured, so only the catalog lookup ran") || !strings.Contains(body, "v2fly:anthropic") {
		t.Error("the page doesn't explain")
	}
	if form := h.Login.Get("/routing/discover").Body.String(); !strings.Contains(form, "not configured: set PROXIER_CHROMIUM_URL") {
		t.Error("the form doesn't say Chromium isn't configured")
	}
}

func TestChromiumFailsMidRun(t *testing.T) {
	h, b := newH(t)
	h.Up.V2fly("anthropic", "claude.ai\n")
	refreshCatalog(t, h)
	b.Fail(discoverytest.ErrUnreachable)
	r := wait(t, h, start(t, h, discovery.Start{Website: "claude.ai", Via: discovery.ViaDirect}), discovery.Failed)
	if !strings.Contains(r.Error, "connection refused") || len(r.Suggestions) != 1 || r.FinishedAt.IsZero() {
		t.Fatalf("run: %+v", r)
	}
	ev := h.Events("routing.discovery_failed")
	if len(ev) != 1 || ev[0].Payload["website"] != "claude.ai" || !strings.Contains(ev[0].Payload["error"].(string), "connection refused") {
		t.Errorf("failed event: %+v", ev)
	}
	if n := len(h.Events("job.failed")); n != 0 {
		t.Errorf("%d job.failed recorded", n)
	}
	if n := len(h.Events("routing.discovery_completed")); n != 0 {
		t.Errorf("completed recorded")
	}
	body := h.Login.Get(fmt.Sprintf("/routing/discover/%d", r.ID)).Body.String()
	if !strings.Contains(body, "The visit failed") || !strings.Contains(body, "v2fly:anthropic") {
		t.Error("the page lost the suggestions or the failure")
	}
}

func TestOneRunAtATime(t *testing.T) {
	h, b := newH(t)
	release := b.Block()
	defer release()
	first := start(t, h, discovery.Start{Website: "one.example", Via: discovery.ViaDirect})
	second := start(t, h, discovery.Start{Website: "two.example", Via: discovery.ViaDirect})
	if !b.Entered(10 * time.Second) {
		t.Fatal("no visit started")
	}
	time.Sleep(400 * time.Millisecond) // more than the workers' poll
	if r, _ := h.Mod.Discovery.Get(bg, second); r.State != discovery.Queued {
		t.Fatalf("the second run is %s while the first visits", r.State)
	}
	if v := b.Visits(); len(v) != 1 || v[0].URL != "https://one.example/" {
		t.Fatalf("visits: %+v", v)
	}
	release()
	wait(t, h, first, discovery.Done)
	wait(t, h, second, discovery.Done)
	if v := b.Visits(); len(v) != 2 || v[1].URL != "https://two.example/" {
		t.Errorf("visits: %+v", v)
	}
}

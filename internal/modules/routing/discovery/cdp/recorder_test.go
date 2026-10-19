package cdp

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
)

func sent(id, u string, redirect int64) network.EventRequestWillBeSent {
	e := network.EventRequestWillBeSent{RequestID: network.RequestID(id), Request: &network.Request{URL: u}, Type: network.ResourceTypeDocument}
	if redirect != 0 {
		e.RedirectResponse = &network.Response{Status: redirect}
	}
	return e
}

// Each kind of event comes on its own stream, so an end or a response can be
// handled before the start or the redirects of its request.
func TestRecorderTakesEventsInAnyOrder(t *testing.T) {
	r := newRecorder(10, 0)
	// the document's 200 and its end come before its redirect hops
	r.final["doc"] = 200
	r.requestWillBeSent(sent("doc", "http://a.test/", 0))
	r.requestWillBeSent(sent("doc", "https://a.test/", 301))
	r.requestWillBeSent(sent("doc", "https://www.a.test/", 302))
	// a failure that comes before its start
	r.failed["img"] = "net::ERR_NAME_NOT_RESOLVED"
	r.requestWillBeSent(sent("img", "http://gone.test/x.gif", 0))
	// one still loading
	r.requestWillBeSent(sent("js", "http://b.test/x.js", 0))

	reqs, capped := r.result()
	if capped || len(reqs) != 5 {
		t.Fatalf("%d requests, capped %v: %+v", len(reqs), capped, reqs)
	}
	for i, want := range []int{301, 302, 200, 0, 0} {
		if reqs[i].Status != want {
			t.Errorf("%s: status %d, want %d", reqs[i].URL, reqs[i].Status, want)
		}
	}
	if reqs[3].Failed != "net::ERR_NAME_NOT_RESOLVED" || reqs[2].Failed != "" {
		t.Errorf("failures: %+v", reqs)
	}
	if st := r.status("doc"); st != 200 {
		t.Errorf("document status %d", st)
	}
}

// A request that never ends in the tab's events (a cross-site iframe's
// document, a beacon) doesn't hold the wait: quiet for idleFor is idle.
func TestRecorderIdleWithARequestThatNeverEnds(t *testing.T) {
	r := newRecorder(10, 0)
	r.requestWillBeSent(sent("frame", "https://challenges.test/turnstile", 0))
	ctx, cancel := context.WithTimeout(context.Background(), idleAtMost)
	defer cancel()
	start := time.Now()
	r.waitIdle(ctx)
	if took := time.Since(start); took < idleFor || took > idleFor+time.Second {
		t.Fatalf("waited %v, want about %v", took, idleFor)
	}
}

// A redirect to a host over the budget ends the request there: the hop that
// redirected keeps the redirect's status.
func TestRecorderRedirectOverTheBudget(t *testing.T) {
	r := newRecorder(1, 0)
	r.requestWillBeSent(sent("doc", "http://a.test/", 0))
	r.requestWillBeSent(sent("doc", "http://b.test/", 302))
	r.final["doc"] = 200
	reqs, capped := r.result()
	if !capped || len(reqs) != 1 || reqs[0].Status != 302 {
		t.Fatalf("capped %v: %+v", capped, reqs)
	}
}

// The link tabs run side by side, but the run's hostnames go to them in page
// order: a host first seen on a later page is the one left out.
func TestCombineSharesTheBudgetInPageOrder(t *testing.T) {
	req := func(host string, page int) discovery.Request {
		return discovery.Request{URL: "https://" + host + "/", Host: host, Page: page}
	}
	pages := []loaded{
		{page: discovery.Page{URL: "https://a.test/", Loaded: true}, title: "Home", reqs: []discovery.Request{req("a.test", 0), req("b.test", 0)}},
		{page: discovery.Page{URL: "https://a.test/x", Loaded: true}, title: "X", reqs: []discovery.Request{req("c.test", 1), req("b.test", 1)}},
		{page: discovery.Page{URL: "https://a.test/y", Loaded: true}, reqs: []discovery.Request{req("a.test", 2)}},
	}
	res := combine(pages, 2)
	if res.Title != "Home" || len(res.Pages) != 3 || res.Pages[1].URL != "https://a.test/x" || !res.Capped {
		t.Fatalf("%q %+v capped %v", res.Title, res.Pages, res.Capped)
	}
	var got []string
	for _, q := range res.Requests {
		got = append(got, q.Host)
	}
	if want := []string{"a.test", "b.test", "b.test", "a.test"}; !slices.Equal(got, want) {
		t.Errorf("hosts %v, want %v", got, want)
	}
	if res := combine(pages, 3); res.Capped || len(res.Requests) != 5 {
		t.Errorf("within the budget: capped %v, %d requests", res.Capped, len(res.Requests))
	}
}

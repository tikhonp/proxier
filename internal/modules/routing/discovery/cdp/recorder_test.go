package cdp

import (
	"testing"

	"github.com/chromedp/cdproto/network"
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
	r := newRecorder(10)
	// the document's 200 and its end come before its redirect hops
	r.final["doc"] = 200
	r.end("doc")
	r.requestWillBeSent(sent("doc", "http://a.test/", 0))
	r.requestWillBeSent(sent("doc", "https://a.test/", 301))
	r.requestWillBeSent(sent("doc", "https://www.a.test/", 302))
	// a failure that comes before its start
	r.failed["img"] = "net::ERR_NAME_NOT_RESOLVED"
	r.end("img")
	r.requestWillBeSent(sent("img", "http://gone.test/x.gif", 0))
	// one still loading
	r.requestWillBeSent(sent("js", "http://b.test/x.js", 0))

	if len(r.open) != 1 || !r.open["js"] {
		t.Fatalf("in flight: %v", r.open)
	}
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
	r.end("js")
	if len(r.open) != 0 {
		t.Errorf("still in flight: %v", r.open)
	}
}

// A redirect to a host over the budget ends the request there: the hop that
// redirected keeps the redirect's status, and nothing waits for the rest.
func TestRecorderRedirectOverTheBudget(t *testing.T) {
	r := newRecorder(1)
	r.requestWillBeSent(sent("doc", "http://a.test/", 0))
	r.requestWillBeSent(sent("doc", "http://b.test/", 302))
	r.final["doc"] = 200
	reqs, capped := r.result()
	if !capped || len(reqs) != 1 || reqs[0].Status != 302 || len(r.open) != 0 {
		t.Fatalf("capped %v, in flight %v: %+v", capped, r.open, reqs)
	}
}

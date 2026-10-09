package discovery_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
)

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		host string
		want discovery.Class
	}{
		{"www.google-analytics.com", discovery.Tracker},
		{"mc.yandex.ru", discovery.Tracker},
		{"cdn.jsdelivr.net", discovery.CDN},
		{"d1.cloudfront.net", discovery.CDN},
		{"203.0.113.5", discovery.IP},
		{"2001:db8::1", discovery.IP},
		{"example.com", discovery.FirstParty},
		{"static.example.com", discovery.FirstParty},
		{"example.net", discovery.ThirdParty},
		{"notexample.com", discovery.ThirdParty},
	} {
		if got := discovery.Classify(c.host, "example.com"); got != c.want {
			t.Errorf("Classify(%s) = %s, want %s", c.host, got, c.want)
		}
	}
	// the site's own hosts win over the built-in lists
	if got := discovery.Classify("www.cloudflare.com", "cloudflare.com"); got != discovery.FirstParty {
		t.Errorf("cloudflare.com's own host: %s", got)
	}

	// ticked by default: the first-party group and hosts that failed
	// directly; trackers, CDNs and IP literals not, IPs can't be
	h, b := newH(t)
	url := "https://example.com/"
	b.Site(url, "", loaded(url, "Example",
		req(url, "example.com"), req("https://static.example.com/a.js", "static.example.com"),
		req("https://www.google-analytics.com/g.js", "www.google-analytics.com"),
		req("https://cdn.jsdelivr.net/x.js", "cdn.jsdelivr.net"),
		req("https://203.0.113.5/p", "203.0.113.5"),
		failed("https://api.other.net/x", "api.other.net", "net::ERR_CONNECTION_RESET")))
	r := wait(t, h, start(t, h, discovery.Start{Website: "example.com", Via: discovery.ViaDirect}), discovery.Done)
	if g, ok := groupOf(r, "example.com"); !ok || !g.Ticked || g.Class != discovery.FirstParty || len(g.Hosts) != 2 {
		t.Errorf("first-party group: %+v", g)
	}
	for _, reg := range []string{"google-analytics.com", "jsdelivr.net", "other.net"} {
		if g, ok := groupOf(r, reg); !ok || g.Ticked {
			t.Errorf("group %s: %+v", reg, g)
		}
	}
	if hst, _ := hostOf(r, "www.google-analytics.com"); hst.Class != discovery.Tracker || hst.Ticked() {
		t.Errorf("tracker: %+v", hst)
	}
	if hst, _ := hostOf(r, "cdn.jsdelivr.net"); hst.Class != discovery.CDN || hst.Ticked() {
		t.Errorf("cdn: %+v", hst)
	}
	if hst, _ := hostOf(r, "api.other.net"); hst.Class != discovery.ThirdParty || !hst.Ticked() || hst.Failed != "net::ERR_CONNECTION_RESET" {
		t.Errorf("failed third-party: %+v", hst)
	}
	if len(r.IPs) != 1 || r.IPs[0].Host != "203.0.113.5" || r.IPs[0].Tickable() || r.IPs[0].Requests != 1 {
		t.Errorf("IP literals: %+v", r.IPs)
	}
	if _, ok := groupOf(r, "203.0.113.5"); ok {
		t.Error("an IP literal is a group")
	}
}

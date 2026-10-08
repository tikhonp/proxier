package alerts_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/alerts"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/geoip"
)

// lookups is an httptest country service: it answers /{ip}/country from its
// map (500 for an address it doesn't know) and remembers what was asked.
type lookups struct {
	mu      sync.Mutex
	answers map[string]string
	asked   []string
}

func (l *lookups) serve(t *testing.T, h *substest.Harness) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/country")
		l.mu.Lock()
		l.asked = append(l.asked, ip)
		cc, ok := l.answers[ip]
		l.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(cc + "\n"))
	}))
	t.Cleanup(srv.Close)
	old := geoip.Client
	geoip.Client = srv.Client()
	t.Cleanup(func() { geoip.Client = old })
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.network_country_url": srv.URL + "/{ip}/country"}); err != nil {
		t.Fatal(err)
	}
}

func (l *lookups) questions() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.asked)
}

func lookupJobs(t *testing.T, h *substest.Harness, state string) int {
	t.Helper()
	var n int
	q := `SELECT count(*) FROM jobs WHERE type = ?`
	args := []any{alerts.JobCountries}
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	if err := h.App.DB.R.GetContext(bg, &n, q, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

func country(t *testing.T, h *substest.Harness, network string) (string, bool) {
	t.Helper()
	var cc []string
	if err := h.App.DB.R.SelectContext(bg, &cc, `SELECT country FROM subs_network_countries WHERE network = ?`, network); err != nil {
		t.Fatal(err)
	}
	if len(cc) == 0 {
		return "", false
	}
	return cc[0], true
}

func TestNetworkCountryLookup(t *testing.T) {
	h := substest.New(t)
	l := &lookups{answers: map[string]string{"198.51.100.0": "nl", "2001:db8:4f2::": "DE"}}
	l.serve(t, h)
	_, token := h.Link(h.Subscription("Friends", 1), "Alex")

	h.Fetch("GET", "/s/"+token, "198.51.100.23:5000", "Happ/1.6")
	if n := lookupJobs(t, h, "queued"); n != 1 {
		t.Fatalf("a new network queued %d lookups", n)
	}
	// later fetches from new networks merge into the queued job
	h.Fetch("GET", "/s/"+token, "198.51.100.99:5000", "Happ/1.6")
	h.Fetch("GET", "/s/"+token, "[2001:db8:4f2:7::9]:5000", "v2RayTun/4.0")
	if n := lookupJobs(t, h, ""); n != 1 {
		t.Fatalf("%d lookup jobs", n)
	}
	h.StartJobs()
	h.Drain()

	asked := l.questions()
	slices.Sort(asked)
	if !slices.Equal(asked, []string{"198.51.100.0", "2001:db8:4f2::"}) {
		t.Errorf("asked %q: the networks' first addresses, once each", asked)
	}
	for _, q := range asked {
		if q == "198.51.100.23" || q == "198.51.100.99" || strings.HasSuffix(q, ":9") {
			t.Errorf("a client's own address was sent: %s", q)
		}
	}
	if cc, _ := country(t, h, "198.51.100.0/24"); cc != "NL" {
		t.Errorf("198.51.100.0/24 → %q", cc)
	}
	if cc, _ := country(t, h, "2001:db8:4f2::/48"); cc != "DE" {
		t.Errorf("2001:db8:4f2::/48 → %q", cc)
	}
	// a known network queues nothing
	h.Fetch("GET", "/s/"+token, "198.51.100.5:5000", "Happ/1.6")
	h.Drain()
	if n := lookupJobs(t, h, ""); n != 1 {
		t.Errorf("a known network queued a lookup: %d jobs", n)
	}
	// the page shows the country next to the network and in the fetch log
	if body := h.Login.Get("/links/1").Body.String(); !strings.Contains(body, "🇳🇱 NL") || !strings.Contains(body, "🇩🇪 DE") {
		t.Errorf("the link page lacks the countries")
	}
}

func TestFailedLookupRetriesLater(t *testing.T) {
	h := substest.New(t)
	l := &lookups{answers: map[string]string{}}
	l.serve(t, h)
	_, token := h.Link(h.Subscription("Friends", 1), "Alex")
	h.StartJobs()

	h.Fetch("GET", "/s/"+token, "198.51.100.23:5000", "Happ/1.6")
	h.Drain()
	if cc, ok := country(t, h, "198.51.100.0/24"); !ok || cc != "" {
		t.Fatalf("a failed lookup stored %q %v", cc, ok)
	}
	h.Fetch("GET", "/s/"+token, "198.51.100.24:5000", "Happ/1.6")
	h.Drain()
	if n := lookupJobs(t, h, ""); n != 1 {
		t.Fatalf("the unknown row queued another lookup: %d", n)
	}

	// a day later prune removes the row, and the next fetch asks again
	h.Advance(23 * time.Hour)
	if err := h.Mod.Links.PruneStep(bg, h.Now); err != nil {
		t.Fatal(err)
	}
	if _, ok := country(t, h, "198.51.100.0/24"); !ok {
		t.Fatal("pruned before a day")
	}
	h.Advance(time.Hour)
	if err := h.Mod.Links.PruneStep(bg, h.Now); err != nil {
		t.Fatal(err)
	}
	if _, ok := country(t, h, "198.51.100.0/24"); ok {
		t.Fatal("the unknown row outlived a day")
	}
	l.mu.Lock()
	l.answers["198.51.100.0"] = "NL"
	l.mu.Unlock()
	h.Fetch("GET", "/s/"+token, "198.51.100.25:5000", "Happ/1.6")
	h.Drain()
	if n := lookupJobs(t, h, ""); n != 2 {
		t.Errorf("after prune: %d lookup jobs", n)
	}
	if cc, _ := country(t, h, "198.51.100.0/24"); cc != "NL" {
		t.Errorf("retried lookup: %q", cc)
	}

	// lookups off: a new network queues nothing
	if err := h.App.Settings.Set(bg, "admin", "subscriptions", map[string]string{"subscriptions.network_country_url": ""}); err != nil {
		t.Fatal(err)
	}
	h.Fetch("GET", "/s/"+token, "203.0.113.5:5000", "Happ/1.6")
	h.Drain()
	if n := lookupJobs(t, h, ""); n != 2 {
		t.Errorf("lookups off: %d jobs", n)
	}
}

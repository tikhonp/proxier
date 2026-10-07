package geoip_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/geoip"
)

func TestCountryLookup(t *testing.T) {
	var hits atomic.Int32
	var path atomic.Value
	answer := "nl\n"
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		path.Store(r.URL.Path)
		if answer == "hang" {
			select {
			case <-time.After(10 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	geoip.Client = srv.Client()
	pattern := srv.URL + "/{ip}/country"

	if got := geoip.Lookup(t.Context(), pattern, "198.51.100.7"); got != "NL" {
		t.Errorf("nl\\n → %q", got)
	}
	if p := path.Load(); p != "/198.51.100.7/country" {
		t.Errorf("the request went to %v", p)
	}

	for name, c := range map[string]struct {
		answer string
		status int
	}{
		"garbage":    {"<html>rate limited</html>", 200},
		"three":      {"NLD", 200},
		"digits":     {"n1", 200},
		"empty":      {"", 200},
		"not found":  {"NL", 404},
		"rate limit": {"NL", 429},
	} {
		answer, status = c.answer, c.status
		if got := geoip.Lookup(t.Context(), pattern, "198.51.100.7"); got != "" {
			t.Errorf("%s → %q", name, got)
		}
	}
	answer, status = "NL", 200

	// no request at all when it cannot make sense
	before := hits.Load()
	for name, c := range map[string][2]string{
		"empty pattern": {"", "198.51.100.7"},
		"no {ip}":       {srv.URL + "/country", "198.51.100.7"},
		"bad ip":        {pattern, "not-an-ip"},
		"hostname":      {pattern, "nl-1.example.com"},
	} {
		if got := geoip.Lookup(t.Context(), c[0], c[1]); got != "" {
			t.Errorf("%s → %q", name, got)
		}
	}
	if hits.Load() != before {
		t.Errorf("%d requests were made for nothing", hits.Load()-before)
	}

	// a service that hangs costs the form no more than the timeout
	answer = "hang"
	start := time.Now()
	if got := geoip.Lookup(t.Context(), pattern, "198.51.100.7"); got != "" {
		t.Errorf("hang → %q", got)
	}
	if d := time.Since(start); d > geoip.Timeout+time.Second {
		t.Errorf("the lookup waited %s", d)
	}
	// the service being down
	srv.Close()
	if got := geoip.Lookup(t.Context(), pattern, "198.51.100.7"); got != "" {
		t.Errorf("down → %q", got)
	}
}

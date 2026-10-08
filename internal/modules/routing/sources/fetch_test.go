package sources_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/sources/sourcestest"
	"github.com/tikhonp/proxier/internal/platform/obs"
)

func TestFetcherAgentAndTimeout(t *testing.T) {
	up := sourcestest.New(t)
	up.V2fly("a", "a.com\ninclude:b\n")
	up.V2fly("b", "b.com\n")
	up.Site("main", "g", "s", "s.com")
	r := resolver(up)
	for _, s := range []string{"v2fly:a", "iplist:s", "iplist:missing", up.File("l.txt", "l.com")} {
		if _, err := r.Resolve(context.Background(), parse(t, s)); err != nil && !strings.Contains(s, "missing") {
			t.Fatal(err)
		}
	}
	agents := up.Agents()
	if len(agents) < 6 {
		t.Fatalf("%d requests", len(agents))
	}
	for _, a := range agents {
		if a != "proxier/"+obs.AppVersion {
			t.Errorf("user agent %q", a)
		}
	}

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(hang.Close)
	f := &sources.Fetcher{Timeout: 50 * time.Millisecond}
	start := time.Now()
	if _, _, err := f.Get(context.Background(), hang.URL, 100); err == nil {
		t.Error("a hanging server answered")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the timeout took %v", d)
	}
	if _, _, err := f.Get(context.Background(), "ftp://example.com/x", 100); err == nil {
		t.Error("ftp was fetched")
	}
}

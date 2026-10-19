package cdp_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery/cdp"
)

// TestRealChromium needs a running Chromium: PROXIER_TEST_CHROMIUM_URL
// ("http://127.0.0.1:9222"). When Chromium runs in a container,
// PROXIER_TEST_PAGE_HOST is how it reaches this machine
// ("host.docker.internal"); the page listens on every address.
func TestRealChromium(t *testing.T) {
	raw := os.Getenv("PROXIER_TEST_CHROMIUM_URL")
	if raw == "" {
		t.Skip("PROXIER_TEST_CHROMIUM_URL is not set")
	}
	pageHost := os.Getenv("PROXIER_TEST_PAGE_HOST")
	if pageHost == "" {
		pageHost = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>Test page</title><h1>hello</h1>
<img src="/pixel.gif"><img src="/moved.gif"><script src="http://no-such-host.invalid/x.js"></script>
<a href="/a">a</a><a href="/b#top">b</a><a href="/b">b again</a><a href="http://elsewhere.invalid/">away</a>`))
	})
	for _, p := range []string{"a", "b"} {
		mux.HandleFunc("/"+p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<!doctype html><title>` + p + `</title><img src="/pixel.gif?` + p + `">`))
		})
	}
	mux.HandleFunc("/moved.gif", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pixel.gif?moved", http.StatusFound)
	})
	mux.HandleFunc("/pixel.gif", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	b, err := cdp.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	v, err := b.Version(ctx)
	if err != nil || !strings.Contains(v, "Chrome") {
		t.Fatalf("version %q: %v", v, err)
	}
	page := "http://" + net.JoinHostPort(pageHost, port) + "/"
	res, err := b.Visit(ctx, discovery.Visit{URL: page, Path: "direct", Links: discovery.Links, MaxHosts: 300, Site: pageHost, PageTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "Test page" || len(res.Pages) != 3 || res.Pages[1].URL != page+"a" || res.Pages[2].URL != page+"b" {
		t.Fatalf("result: %q %+v", res.Title, res.Pages)
	}
	for _, pg := range res.Pages {
		if !pg.Loaded || pg.Status != 200 {
			t.Errorf("page %s: loaded %v, status %d", pg.URL, pg.Loaded, pg.Status)
		}
	}
	if !bytes.HasPrefix(res.Pages[0].Screenshot, []byte{0xff, 0xd8}) {
		t.Error("the screenshot isn't a JPEG")
	}
	var gif, moved, landed, failed bool
	for _, q := range res.Requests {
		for i, p := range []string{"", "a", "b"} {
			if q.URL == page+p && q.Page != i {
				t.Errorf("%s is on page %d, want %d", q.URL, q.Page, i)
			}
		}
		gif = gif || strings.HasSuffix(q.URL, "/pixel.gif") && q.Status == 200 && q.Host == pageHost
		moved = moved || strings.HasSuffix(q.URL, "/moved.gif") && q.Status == 302
		landed = landed || strings.HasSuffix(q.URL, "/pixel.gif?moved") && q.Status == 200
		failed = failed || q.Host == "no-such-host.invalid" && strings.HasPrefix(q.Failed, "net::ERR_")
	}
	if !gif || !moved || !landed || !failed {
		t.Errorf("requests: %+v", res.Requests)
	}
}

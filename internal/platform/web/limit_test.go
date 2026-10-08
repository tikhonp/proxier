package web_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestPublicRateLimit(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{NoAdmin: true})
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	lim := s.App.PublicLimit
	lim.Now = func() time.Time { return now }
	if lim.Max != 60 || lim.Window != time.Minute {
		t.Fatalf("limit %d per %s", lim.Max, lim.Window)
	}
	get := func(path, addr string) int { return s.Do(sitetest.Req{Path: path, Addr: addr}).Code }
	// unknown paths under /s/, /r/ and /f/ count: guessing tokens is limited too
	for i := 0; i < 60; i++ {
		p := []string{"/s/", "/r/", "/f/"}[i%3] + fmt.Sprint("guess", i)
		if code := get(p, "198.51.100.1:1000"); code != 404 {
			t.Fatalf("request %d: %d", i+1, code)
		}
	}
	now = now.Add(15 * time.Second)
	rec := s.Do(sitetest.Req{Path: "/s/one-more", Addr: "198.51.100.1:1001"})
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "45" || rec.Body.String() != "Too Many Requests\n" {
		t.Fatalf("61st: %d %q %q", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Set-Cookie") != "" {
		t.Error("a 429 must be a public answer")
	}
	if code := get("/s/other", "198.51.100.2:1000"); code != 404 {
		t.Errorf("another IP: %d", code)
	}
	// /agent/ limits itself per session; admin pages aren't counted
	for i := 0; i < 100; i++ {
		if code := get("/agent/v1/context", "198.51.100.1:1000"); code == 429 {
			t.Fatal("/agent/ was limited")
		}
	}
	if code := get("/login", "198.51.100.1:1000"); code != 200 {
		t.Errorf("/login: %d", code)
	}
	// the next window serves again; less than a second left still says 1
	now = now.Add(44*time.Second + 500*time.Millisecond)
	if rec := s.Do(sitetest.Req{Path: "/s/x", Addr: "198.51.100.1:1000"}); rec.Code != 429 || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("half a second left: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	now = now.Add(time.Second)
	if code := get("/s/x", "198.51.100.1:1000"); code != 404 {
		t.Errorf("the next window: %d", code)
	}
	// Max 0 is off
	lim.Max = 0
	for i := 0; i < 200; i++ {
		if code := get("/s/x", "198.51.100.3:1000"); code != 404 {
			t.Fatalf("off: %d", code)
		}
	}
}

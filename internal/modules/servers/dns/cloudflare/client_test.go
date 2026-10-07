package cloudflare_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare/cloudflaretest"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

const (
	token = "cf-token-SECRET-123"
	zone  = "tikhonnnnn.com"
	name  = "nl-1.hosts.tikhonnnnn.com"
)

func TestEnsureCreatesThenReuses(t *testing.T) {
	f := cloudflaretest.New(t, token, zone, "example.org")
	drv, _ := f.Driver(t, zone)

	rec, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Provider != "cloudflare" || rec.Zone != zone || rec.ZoneID != f.ZoneID(zone) || rec.Name != name || rec.Type != "A" || rec.Content != "198.51.100.7" || rec.RecordID == "" {
		t.Errorf("record: %+v", rec)
	}
	if got := f.Records(zone); len(got) != 1 || got[0].ID != rec.RecordID {
		t.Fatalf("stored: %+v", got)
	}

	// A retry of the same server finds its own record: nothing new, the same id.
	again, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false)
	if err != nil || again.RecordID != rec.RecordID || len(f.Records(zone)) != 1 {
		t.Fatalf("retry: %+v %v (%d records)", again, err, len(f.Records(zone)))
	}
	if f.Count("POST /zones/{zone}/dns_records") != 1 || f.Count("PUT /zones/{zone}/dns_records/{id}") != 0 {
		t.Errorf("an unchanged retry should write nothing more: %v", f.Calls())
	}

	// ...and moves it when the server got another IP
	moved, err := drv.Ensure(t.Context(), name, "198.51.100.9", "nl-1", false)
	if err != nil || moved.RecordID != rec.RecordID {
		t.Fatalf("move: %+v %v", moved, err)
	}
	if got := f.Records(zone); len(got) != 1 || got[0].Content != "198.51.100.9" {
		t.Errorf("after the move: %+v", got)
	}
}

func TestEnsureConflictAndOverwrite(t *testing.T) {
	f := cloudflaretest.New(t, token, zone)
	drv, _ := f.Driver(t, zone)
	id := f.AddRecord(zone, name, "203.0.113.5", "my own record")

	_, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false)
	var conflict *dns.ConflictError
	if !errors.As(err, &conflict) || conflict.Content != "203.0.113.5" || conflict.Comment != "my own record" || conflict.Name != name {
		t.Fatalf("conflict: %v", err)
	}
	if got := f.Records(zone); got[0].Content != "203.0.113.5" || got[0].Comment != "my own record" {
		t.Fatalf("the foreign record was touched: %+v", got)
	}

	// another server's record is foreign too
	f.Change(id, func(r *cloudflaretest.Record) { r.Comment = "proxier:de-2" })
	if _, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false); !errors.As(err, &conflict) || conflict.Comment != "proxier:de-2" {
		t.Errorf("another server's record: %v", err)
	}

	rec, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", true)
	if err != nil || rec.RecordID != id {
		t.Fatalf("overwrite: %+v %v", rec, err)
	}
	got := f.Records(zone)
	if len(got) != 1 || got[0].Content != "198.51.100.7" || got[0].Comment != "proxier:nl-1" {
		t.Errorf("overwritten in place and marked: %+v", got)
	}

	// two foreign records: both are listed; overwrite leaves one
	f2 := cloudflaretest.New(t, token, zone)
	drv2, _ := f2.Driver(t, zone)
	f2.AddRecord(zone, name, "203.0.113.5", "")
	f2.AddRecord(zone, name, "203.0.113.6", "")
	if _, err := drv2.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false); !errors.As(err, &conflict) || conflict.Content != "203.0.113.5, 203.0.113.6" {
		t.Errorf("two records: %v", err)
	}
	if _, err := drv2.Ensure(t.Context(), name, "198.51.100.7", "nl-1", true); err != nil {
		t.Fatal(err)
	}
	if got := f2.Records(zone); len(got) != 1 || got[0].Content != "198.51.100.7" {
		t.Errorf("overwriting two: %+v", got)
	}
}

func TestRecordShape(t *testing.T) {
	f := cloudflaretest.New(t, token, zone)
	drv, _ := f.Driver(t, zone)
	if _, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false); err != nil {
		t.Fatal(err)
	}
	r := f.Records(zone)[0]
	if r.Proxied || r.TTL != 60 || r.Comment != "proxier:nl-1" || r.Name != name || r.Content != "198.51.100.7" {
		t.Errorf("record: %+v", r)
	}
	// a retry of our own record that someone proxied is put back to DNS-only
	f.Change(r.ID, func(r *cloudflaretest.Record) { r.Proxied, r.TTL = true, 1 })
	if _, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false); err != nil {
		t.Fatal(err)
	}
	if r := f.Records(zone)[0]; r.Proxied || r.TTL != 60 {
		t.Errorf("not restored: %+v", r)
	}
}

func TestTokenRejected(t *testing.T) {
	f := cloudflaretest.New(t, token, zone)
	f.RejectToken()
	drv, _ := f.Driver(t, zone)
	_, err := drv.Ensure(t.Context(), name, "198.51.100.7", "nl-1", false)
	if !errors.Is(err, cloudflare.ErrTokenRejected) || !strings.Contains(err.Error(), "on "+zone) {
		t.Fatalf("Ensure: %v", err)
	}
	if !jobs.IsPermanent(err) {
		t.Error("a rejected token must not be retried")
	}
	if _, _, err := drv.Remove(t.Context(), dns.Record{ZoneID: f.ZoneID(zone), Zone: zone, RecordID: "x"}, "nl-1", "198.51.100.7"); !errors.Is(err, cloudflare.ErrTokenRejected) || !jobs.IsPermanent(err) {
		t.Errorf("Remove: %v", err)
	}
	// the client alone: 403 is not retried
	if err := f.Client().VerifyToken(t.Context()); !errors.Is(err, cloudflare.ErrTokenRejected) {
		t.Errorf("VerifyToken: %v", err)
	}
	if len(f.Slept()) != 0 {
		t.Errorf("a refused token was retried: %v", f.Slept())
	}
	// 401 reads the same
	f2 := cloudflaretest.New(t, token, zone)
	f2.Fail("GET /zones", 401)
	if _, err := f2.Client().Zones(t.Context()); !errors.Is(err, cloudflare.ErrTokenRejected) {
		t.Errorf("401: %v", err)
	}
}

func TestRetries(t *testing.T) {
	f := cloudflaretest.New(t, token, zone)
	c := f.Client()

	// 429 waits what Cloudflare asks and retries
	f.RateLimit("GET /zones", "7", 2)
	zs, err := c.Zones(t.Context())
	if err != nil || len(zs) != 1 {
		t.Fatalf("429: %v %v", zs, err)
	}
	if got := f.Slept(); len(got) != 2 || got[0] != 7*time.Second || got[1] != 7*time.Second {
		t.Errorf("waits after 429: %v", got)
	}

	// a Retry-After that is missing or absurd means ten seconds
	f2 := cloudflaretest.New(t, token, zone)
	f2.RateLimit("GET /zones", "", 1)
	f2.RateLimit("GET /zones", "3600", 1)
	if _, err := f2.Client().Zones(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f2.Slept(); len(got) != 2 || got[0] != 10*time.Second || got[1] != 10*time.Second {
		t.Errorf("default waits: %v", got)
	}

	// endless 429 gives up after five retries
	f3 := cloudflaretest.New(t, token, zone)
	f3.RateLimit("GET /zones", "1", 50)
	if _, err := f3.Client().Zones(t.Context()); err == nil {
		t.Error("endless 429 succeeded")
	}
	if n := f3.Count("GET /zones"); n != 6 {
		t.Errorf("%d attempts at endless 429, want 1+5", n)
	}

	// 5xx: three retries at 1, 3 and 9 s, then the error
	f4 := cloudflaretest.New(t, token, zone)
	f4.Fail("GET /zones", 502, 500, 503, 500)
	_, err = f4.Client().Zones(t.Context())
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("5xx: %v", err)
	}
	if got := f4.Slept(); len(got) != 3 || got[0] != time.Second || got[1] != 3*time.Second || got[2] != 9*time.Second {
		t.Errorf("waits after 5xx: %v", got)
	}
	if n := f4.Count("GET /zones"); n != 4 {
		t.Errorf("%d attempts at 5xx, want 1+3", n)
	}

	// a 5xx that clears on the third try is fine
	f5 := cloudflaretest.New(t, token, zone)
	f5.Fail("GET /zones", 500, 500)
	if _, err := f5.Client().Zones(t.Context()); err != nil {
		t.Errorf("5xx then ok: %v", err)
	}

	// a cancel ends the wait
	f6 := cloudflaretest.New(t, token, zone)
	f6.Fail("GET /zones", 500)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f6.Client().Zones(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestErrorsHideToken(t *testing.T) {
	f := cloudflaretest.New(t, token, zone)
	f.EchoAuth = true // Cloudflare's messages here repeat the Authorization header
	c := f.Client()
	f.Fail("GET /zones", 400)
	_, err := c.Zones(t.Context())
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the token is in the error: %v", err)
	}
	if !strings.Contains(err.Error(), "scripted failure") {
		t.Errorf("Cloudflare's message is lost: %v", err)
	}
	// a network failure too: the URL carries no secret and neither does the error
	f.Fail("GET /zones", 500, 500, 500, 500)
	if _, err := c.Zones(t.Context()); err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("5xx: %v", err)
	}
	dead := cloudflaretest.New(t, token, zone)
	c2 := dead.Client()
	c2.BaseURL = "http://127.0.0.1:1/" + token // even a token in the URL must not leak
	if _, err := c2.Zones(t.Context()); err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestZonesAreListedAcrossPages(t *testing.T) {
	names := []string{"a.com", "b.com", "c.com", "d.com", "e.com"}
	f := cloudflaretest.New(t, token, names...)
	f.PerPage = 2
	zs, err := f.Client().Zones(t.Context())
	if err != nil || len(zs) != 5 || zs[4].Name != "e.com" {
		t.Fatalf("%v %v", zs, err)
	}
	if f.Count("GET /zones") != 3 {
		t.Errorf("calls: %v", f.Calls())
	}
}

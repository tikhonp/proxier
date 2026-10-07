package dns_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare/cloudflaretest"
)

func TestZoneByLongestSuffix(t *testing.T) {
	zones := []string{"tikhonnnnn.com", "hosts.tikhonnnnn.com", "example.org"}
	for name, want := range map[string]string{
		"nl-1.hosts.tikhonnnnn.com":  "hosts.tikhonnnnn.com", // the longer wins
		"proxier.tikhonnnnn.com":     "tikhonnnnn.com",
		"tikhonnnnn.com":             "tikhonnnnn.com", // the zone's own apex
		"NL-1.Hosts.Tikhonnnnn.com.": "hosts.tikhonnnnn.com",
		"deep.sub.example.org":       "example.org",
		"nottikhonnnnn.com":          "", // a suffix of the text is not a parent domain
		"tikhonnnnn.com.evil.net":    "",
		"other.net":                  "",
		"":                           "",
	} {
		got, ok := dns.ZoneFor(name, zones)
		if got != want || ok != (want != "") {
			t.Errorf("ZoneFor(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if z, ok := dns.ZoneFor("a.example.org", nil); ok || z != "" {
		t.Errorf("no zones allowed: %q %v", z, ok)
	}
	if got := dns.SplitZones(" a.com, ,b.org ,"); len(got) != 2 || got[0] != "a.com" || got[1] != "b.org" {
		t.Errorf("SplitZones: %q", got)
	}

	// the driver's form check applies it, and only to allowed zones
	f := cloudflaretest.New(t, "tok", "tikhonnnnn.com", "example.org")
	drv, _ := f.Driver(t, "tikhonnnnn.com")
	if z, ok, err := drv.Covers(t.Context(), "nl-1.hosts.tikhonnnnn.com"); err != nil || !ok || z != "tikhonnnnn.com" {
		t.Errorf("Covers: %q %v %v", z, ok, err)
	}
	if _, ok, err := drv.Covers(t.Context(), "nl-1.example.org"); err != nil || ok {
		t.Errorf("a zone the token sees but the admin did not allow: %v %v", ok, err)
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("Covers must not call Cloudflare: %v", f.Calls())
	}
}

func TestRemoveOnlyOwnRecords(t *testing.T) {
	const zone, name, ip = "tikhonnnnn.com", "nl-1.hosts.tikhonnnnn.com", "198.51.100.7"
	f := cloudflaretest.New(t, "tok", zone)
	drv, _ := f.Driver(t, zone)
	ensure := func() dns.Record {
		t.Helper()
		rec, err := drv.Ensure(t.Context(), name, ip, "nl-1", true)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}

	// its own, unchanged: deleted
	rec := ensure()
	removed, kept, err := drv.Remove(t.Context(), rec, "nl-1", ip)
	if err != nil || !removed || kept.Reason != "" || len(f.Records(zone)) != 0 {
		t.Fatalf("own record: %v %+v %v (%d left)", removed, kept, err, len(f.Records(zone)))
	}

	// 404: already gone counts as removed
	removed, _, err = drv.Remove(t.Context(), rec, "nl-1", ip)
	if err != nil || !removed {
		t.Errorf("gone: %v %v", removed, err)
	}

	// now pointing elsewhere: kept, with the address
	rec = ensure()
	f.Change(rec.RecordID, func(r *cloudflaretest.Record) { r.Content = "203.0.113.99" })
	removed, kept, err = drv.Remove(t.Context(), rec, "nl-1", ip)
	if err != nil || removed || kept.Reason != "points to 203.0.113.99" || len(f.Records(zone)) != 1 {
		t.Errorf("moved: %v %+v %v", removed, kept, err)
	}

	// the comment was changed: kept
	f.Change(rec.RecordID, func(r *cloudflaretest.Record) { r.Content, r.Comment = ip, "mine now" })
	removed, kept, err = drv.Remove(t.Context(), rec, "nl-1", ip)
	if err != nil || removed || kept.Reason != "comment changed" || len(f.Records(zone)) != 1 {
		t.Errorf("recommented: %v %+v %v", removed, kept, err)
	}

	// another server's name on the same record: kept
	f.Change(rec.RecordID, func(r *cloudflaretest.Record) { r.Comment = "proxier:de-2" })
	if removed, kept, _ := drv.Remove(t.Context(), rec, "nl-1", ip); removed || kept.Reason != "comment changed" {
		t.Errorf("another server's: %v %+v", removed, kept)
	}

	// the delete itself racing with a deletion elsewhere is still success
	f.Change(rec.RecordID, func(r *cloudflaretest.Record) { r.Comment = "proxier:nl-1" })
	f.Fail("DELETE /zones/{zone}/dns_records/{id}", 404)
	if removed, _, err := drv.Remove(t.Context(), rec, "nl-1", ip); err != nil || !removed {
		t.Errorf("delete answered 404: %v %v", removed, err)
	}
}

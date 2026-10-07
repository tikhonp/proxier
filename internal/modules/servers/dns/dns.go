// Package dns is how Proxier gets a server's hostname to point at its IP: a
// Driver writes the record at a DNS provider, a Waiter then watches public
// resolvers until they all see it (docs/integrations/cloudflare.md). Cloudflare
// is the only driver.
package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Record is a DNS record Proxier created, as stored with the server.
type Record struct {
	Provider, ZoneID, Zone, Name, Type, Content, RecordID string
}

// ConflictError says a record Proxier did not create already sits at the name.
// Provisioning stops with it and shows the existing value; Overwrite is the
// admin's explicit choice.
type ConflictError struct{ Name, Content, Comment string }

func (e *ConflictError) Error() string {
	if e.Comment != "" {
		return fmt.Sprintf("dns: %s already points to %s (comment %q)", e.Name, e.Content, e.Comment)
	}
	return fmt.Sprintf("dns: %s already points to %s", e.Name, e.Content)
}

// ErrNotVisible: the record was written but the resolvers do not all show it.
var ErrNotVisible = errors.New("dns: the record is not visible on every resolver yet")

// ErrNotCovered: the name is in none of the zones Proxier may use.
var ErrNotCovered = errors.New("dns: the name is in none of the allowed zones")

// ErrNotConfigured: the provider has no token yet.
var ErrNotConfigured = errors.New("dns: the DNS provider is not configured")

// Kept is why Remove left a record alone.
type Kept struct{ Reason string } // "points to 198.51.100.7", "comment changed"

// Driver writes and removes the A records of servers.
type Driver interface {
	// Ensure makes name point to ip. See the provider for the rules; the
	// common one: a record Proxier did not create is a *ConflictError unless
	// overwrite is set.
	Ensure(ctx context.Context, name, ip, serverName string, overwrite bool) (Record, error)
	// Remove deletes r only while it still points to ip and carries Proxier's
	// mark for serverName; otherwise it is left and Kept says why. A record
	// that is already gone counts as removed.
	Remove(ctx context.Context, r Record, serverName, ip string) (removed bool, kept Kept, err error)
	// Covers is the form's check: the zone the name belongs to, if it is one
	// the admin allowed.
	Covers(ctx context.Context, name string) (zone string, ok bool, err error)
}

// Comment is the mark on every record Proxier writes.
func Comment(serverName string) string { return "proxier:" + serverName }

// ZoneFor picks the zone for name: the longest of zones that name is, or is a
// subdomain of. Comparison ignores case and a trailing dot.
func ZoneFor(name string, zones []string) (string, bool) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	best := ""
	for _, z := range zones {
		z = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(z), "."))
		if z == "" {
			continue
		}
		if (name == z || strings.HasSuffix(name, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best, best != ""
}

// SplitZones turns the comma-separated setting into a list.
func SplitZones(s string) []string {
	var out []string
	for _, z := range strings.Split(s, ",") {
		if z = strings.TrimSpace(z); z != "" {
			out = append(out, z)
		}
	}
	return out
}

package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Settings keys of the integration.
const (
	TokenKey = "cloudflare.api_token"
	ZonesKey = "cloudflare.zones"
)

const provider = "cloudflare"

// Section is the integration's settings section.
var Section = settings.Section{
	Name: "cloudflare", Module: "servers",
	Fields: []settings.Field{
		{Key: TokenKey, Kind: settings.Secret, MaxLen: 200},
		{Key: ZonesKey, Kind: settings.String, MaxLen: 2000},
	},
}

type driver struct {
	st        *settings.Store
	newClient func(token string) *Client
}

// NewDriver is settings-backed: it reads the token and the allowed zones on
// every call, so a change in Settings applies at once.
func NewDriver(st *settings.Store, newClient func(token string) *Client) dns.Driver {
	if newClient == nil {
		newClient = New
	}
	return &driver{st: st, newClient: newClient}
}

func (d *driver) allowed(ctx context.Context) ([]string, error) {
	v, err := d.st.Get(ctx, ZonesKey)
	if err != nil {
		return nil, err
	}
	return dns.SplitZones(v), nil
}

func (d *driver) client(ctx context.Context) (*Client, error) {
	tok, err := d.st.Get(ctx, TokenKey)
	if err != nil {
		return nil, err
	}
	if tok == "" {
		return nil, dns.ErrNotConfigured
	}
	return d.newClient(tok), nil
}

func (d *driver) Covers(ctx context.Context, name string) (string, bool, error) {
	zones, err := d.allowed(ctx)
	if err != nil {
		return "", false, err
	}
	z, ok := dns.ZoneFor(name, zones)
	return z, ok, nil
}

// zone finds the id of the zone name belongs to.
func (d *driver) zone(ctx context.Context, c *Client, name string) (id, zone string, err error) {
	allowed, err := d.allowed(ctx)
	if err != nil {
		return "", "", err
	}
	zone, ok := dns.ZoneFor(name, allowed)
	if !ok {
		return "", "", fmt.Errorf("%w: %s", dns.ErrNotCovered, name)
	}
	zs, err := c.Zones(ctx)
	if err != nil {
		return "", "", d.wrap(err, zone)
	}
	for _, z := range zs {
		if strings.EqualFold(z.Name, zone) {
			return z.ID, z.Name, nil
		}
	}
	return "", "", jobs.Permanent(fmt.Errorf("cloudflare: the token does not see the zone %s", zone))
}

// wrap names the zone in a rejected token's error and marks it permanent.
func (d *driver) wrap(err error, zone string) error {
	if errors.Is(err, ErrTokenRejected) {
		return jobs.Permanent(fmt.Errorf("%w on %s", ErrTokenRejected, zone))
	}
	return err
}

func (d *driver) Ensure(ctx context.Context, name, ip, serverName string, overwrite bool) (dns.Record, error) {
	c, err := d.client(ctx)
	if err != nil {
		return dns.Record{}, err
	}
	zoneID, zone, err := d.zone(ctx, c, name)
	if err != nil {
		return dns.Record{}, err
	}
	mine := dns.Comment(serverName)
	want := A{Name: name, Content: ip, Comment: mine}
	rec := dns.Record{Provider: provider, ZoneID: zoneID, Zone: zone, Name: name, Type: "A", Content: ip}

	existing, err := c.FindA(ctx, zoneID, name)
	if err != nil {
		return dns.Record{}, d.wrap(err, zone)
	}
	switch {
	case len(existing) == 0:
		made, err := c.CreateA(ctx, zoneID, want)
		if err != nil {
			return dns.Record{}, d.wrap(err, zone)
		}
		rec.RecordID = made.ID
		return rec, nil

	case len(existing) == 1 && existing[0].Comment == mine:
		// A retry of this server's own record: bring it to the IP if it moved.
		cur := existing[0]
		rec.RecordID = cur.ID
		if cur.Content != ip || cur.Proxied || cur.TTL != 60 {
			if _, err := c.UpdateA(ctx, zoneID, cur.ID, want); err != nil {
				return dns.Record{}, d.wrap(err, zone)
			}
		}
		return rec, nil
	}

	// Something else is at the name.
	if !overwrite {
		contents := make([]string, len(existing))
		for i, e := range existing {
			contents[i] = e.Content
		}
		return dns.Record{}, &dns.ConflictError{Name: name, Content: strings.Join(contents, ", "), Comment: existing[0].Comment}
	}
	first := existing[0]
	if _, err := c.UpdateA(ctx, zoneID, first.ID, want); err != nil {
		return dns.Record{}, d.wrap(err, zone)
	}
	for _, extra := range existing[1:] { // the name must lead to the one IP
		if err := c.DeleteRecord(ctx, zoneID, extra.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return dns.Record{}, d.wrap(err, zone)
		}
	}
	rec.RecordID = first.ID
	return rec, nil
}

func (d *driver) Remove(ctx context.Context, r dns.Record, serverName, ip string) (bool, dns.Kept, error) {
	c, err := d.client(ctx)
	if err != nil {
		return false, dns.Kept{}, err
	}
	cur, err := c.GetA(ctx, r.ZoneID, r.RecordID)
	switch {
	case errors.Is(err, ErrNotFound):
		return true, dns.Kept{}, nil
	case err != nil:
		return false, dns.Kept{}, d.wrap(err, r.Zone)
	}
	if cur.Content != ip {
		return false, dns.Kept{Reason: "points to " + cur.Content}, nil
	}
	if cur.Comment != dns.Comment(serverName) {
		return false, dns.Kept{Reason: "comment changed"}, nil
	}
	if err := c.DeleteRecord(ctx, r.ZoneID, r.RecordID); err != nil && !errors.Is(err, ErrNotFound) {
		return false, dns.Kept{}, d.wrap(err, r.Zone)
	}
	return true, dns.Kept{}, nil
}

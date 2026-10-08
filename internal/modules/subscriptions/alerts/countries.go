package alerts

import (
	"context"
	"net/netip"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/geoip"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Country lookups look at the networks of the last week, at most
// lookupBatch of them a run.
const (
	lookupSince = 7 * 24 * time.Hour
	lookupBatch = 50
)

// QueueCountry enqueues the lookup inside the fetch's transaction when the
// network has no row and lookups are on. Later fetches merge into the queued
// job.
func (s *Service) QueueCountry(ctx context.Context, tx *sqlx.Tx, network string) error {
	pattern, err := s.d.Settings.Get(ctx, conf.NetworkCountryURL)
	if err != nil || pattern == "" || network == "" {
		return err
	}
	if has, err := store.HasNetworkCountry(ctx, tx, network); err != nil || has {
		return err
	}
	_, err = s.d.Jobs.Enqueue(ctx, tx, jobs.Request{Type: JobCountries, CoalescingKey: "network-countries", CreatedBy: events.ActorSystem})
	return err
}

// FirstAddress is the address a network is looked up by: 198.51.100.0 for
// 198.51.100.0/24, never a client's own address.
func FirstAddress(network string) (string, bool) {
	p, err := netip.ParsePrefix(network)
	if err != nil {
		return "", false
	}
	return p.Masked().Addr().String(), true
}

// LookupCountries looks up the networks of the last week's fetches that have
// no row, then stores every answer in one write ("" when the lookup failed,
// so it is tried again once prune removes the row a day later).
func (s *Service) LookupCountries(ctx context.Context, now time.Time) error {
	pattern, err := s.d.Settings.Get(ctx, conf.NetworkCountryURL)
	if err != nil || pattern == "" {
		return err
	}
	nets, err := store.NetworksToLookUp(ctx, s.d.DB.R, db.At(now.Add(-lookupSince)), lookupBatch)
	if err != nil || len(nets) == 0 {
		return err
	}
	found := make([]string, len(nets))
	for i, n := range nets {
		if addr, ok := FirstAddress(n); ok {
			found[i] = geoip.Lookup(ctx, pattern, addr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	at := db.At(now)
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for i, n := range nets {
			if err := store.PutNetworkCountry(ctx, tx, n, found[i], at); err != nil {
				return err
			}
		}
		return nil
	})
}

package deploy

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// Addresses lists a server's additional addresses in the order templates see
// them.
func (s *Service) Addresses(ctx context.Context, serverID int64) ([]store.Address, error) {
	return store.ServerAddresses(ctx, s.DB.R, serverID)
}

// AddAddress appends an additional address to an active server. Proxier does
// not write its DNS record: the member manages it. The caller redeploys the
// server afterwards.
func (s *Service) AddAddress(ctx context.Context, serverID int64, ip, hostname, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		// Everything is checked here, in the writing transaction: two adds at
		// once, or an add and a create, cannot both pass the same check.
		srv, err := s.activeServer(ctx, tx, serverID)
		if err != nil {
			return err
		}
		have, err := store.ServerAddresses(ctx, tx, srv.ID)
		if err != nil {
			return err
		}
		// The server's own addresses are not additional ones, nor are the
		// ones it already has.
		used := provision.NewAddressSet(srv.IP, srv.ManagementHostname, srv.ProxyHostname)
		for _, a := range have {
			used.IPs[a.IP], used.Hosts[a.Hostname] = true, true
		}
		ip, hostname, errs := provision.CheckAddress(ctx, tx, srv.ID, used, strings.TrimSpace(ip), hostname)
		if len(errs) > 0 {
			return errs
		}
		if _, err := store.InsertAddress(ctx, tx, srv.ID, ip, hostname, db.At(s.now())); err != nil {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{
			Type: "server.address_added", Subject: store.ServerSubject(srv.ID), Actor: actor,
			Payload: map[string]any{"ip": ip, "hostname": hostname},
		})
		return err
	})
}

// RemoveAddress deletes an additional address of an active server. The
// caller redeploys it afterwards; the member's DNS record is untouched.
func (s *Service) RemoveAddress(ctx context.Context, serverID, addressID int64, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		srv, err := s.activeServer(ctx, tx, serverID)
		if err != nil {
			return err
		}
		addr, err := store.GetAddress(ctx, tx, serverID, addressID)
		if err != nil {
			return err
		}
		addrs, err := store.ServerAddresses(ctx, tx, serverID)
		if err != nil {
			return err
		}
		// Endpoints bind to an address by its position, so positions never
		// shift: only the last one can go, and the next add takes its number.
		if len(addrs) > 0 && addr.Position != addrs[len(addrs)-1].Position {
			return FieldErrors{"address": {Key: "servers.err.addr_remove_last"}}
		}
		ok, err := store.DeleteAddress(ctx, tx, serverID, addressID)
		if err != nil {
			return err
		}
		if !ok {
			return store.ErrNotFound
		}
		_, err = s.Events.Record(ctx, tx, events.Event{
			Type: "server.address_removed", Subject: store.ServerSubject(srv.ID), Actor: actor,
			Payload: map[string]any{"ip": addr.IP, "hostname": addr.Hostname},
		})
		return err
	})
}

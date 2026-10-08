package subs

import (
	"context"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// Subscriber reacts to the servers module: an activated server joins the
// subscriptions that add new servers, a retired one leaves every
// subscription. Both are idempotent, so a second delivery records nothing.
func (s *Service) Subscriber() events.Subscriber {
	return events.Subscriber{
		Name:  "subscriptions.servers",
		Types: []string{"server.activated", "server.retired"},
		Handle: func(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
			if e.Subject.Type != "server" {
				return nil
			}
			id, err := strconv.ParseInt(e.Subject.ID, 10, 64)
			if err != nil {
				return nil
			}
			if e.Type == "server.retired" {
				return s.retired(ctx, tx, id)
			}
			return s.activated(ctx, tx, id, e)
		},
	}
}

func (s *Service) activated(ctx context.Context, tx *sqlx.Tx, serverID int64, e events.Event) error {
	if s.d.Catalog == nil {
		return nil
	}
	auto, err := store.AutoAddSubscriptions(ctx, tx)
	if err != nil {
		return err
	}
	var want []store.Subscription
	for _, sub := range auto {
		// only servers activated after the switch was turned on
		if !sub.AutoAddSince.After(e.Time.Time) {
			want = append(want, sub)
		}
	}
	if len(want) == 0 {
		return nil
	}
	// The catalog reads the committed state through the read pool; WAL lets
	// it while this cursor transaction is open.
	se, ok, err := s.d.Catalog.Server(ctx, serverID)
	if err != nil || !ok {
		return err
	}
	for _, sub := range want {
		if _, err := s.append(ctx, tx, sub.ID, []servers.ServerEndpoints{se}, events.ActorSystem, map[string]any{"auto": true}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) retired(ctx context.Context, tx *sqlx.Tx, serverID int64) error {
	subs, err := store.SubscriptionsOfServer(ctx, tx, serverID)
	if err != nil {
		return err
	}
	for _, id := range subs {
		if _, err := s.remove(ctx, tx, id, serverID, events.ActorSystem, map[string]any{"reason": "retired"}); err != nil {
			return err
		}
	}
	return nil
}

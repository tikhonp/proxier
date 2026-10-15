package services

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/access"
)

// Remove deletes a service scope may change with its snapshots and custom
// rows, only when no routing list holds it.
func (s *Service) Remove(ctx context.Context, id int64, scope access.Scope, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := toChange(ctx, tx, id, scope)
		if err != nil {
			return err
		}
		lists, err := store.ListsOf(ctx, tx, id, access.All)
		if err != nil {
			return err
		}
		if len(lists) > 0 {
			return &InListsError{Lists: lists}
		}
		if err := store.DeleteService(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.service_removed", id, actor, map[string]any{"selector": cur.Selector, "tag": cur.Tag})
	})
}

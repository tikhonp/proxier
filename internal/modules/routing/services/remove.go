package services

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// Remove deletes a service with its snapshots and custom rows, only when no
// routing list holds it.
func (s *Service) Remove(ctx context.Context, id int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetService(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		lists, err := store.ListsOf(ctx, tx, id)
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

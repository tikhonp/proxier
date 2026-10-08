package lists

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// targetWord is how HasTargetsError names a target's kind.
var targetWord = map[string]string{"router": "router", "shadowrocket": "Shadowrocket"}

// Delete deletes a list; its services stay. The default can't go
// (ErrDefault). Routers and Shadowrocket configs following it must move to
// another list (moveTo) in the same step, else *HasTargetsError.
func (s *Service) Delete(ctx context.Context, id, moveTo int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		l, err := getList(ctx, tx, id)
		if err != nil {
			return err
		}
		if l.IsDefault {
			return ErrDefault
		}
		targets, err := store.ListTargets(ctx, tx, id)
		if err != nil {
			return err
		}
		var moved []string
		if len(targets) > 0 {
			if moveTo == 0 {
				e := &HasTargetsError{}
				for _, t := range targets {
					e.Targets = append(e.Targets, t.Name+" ("+targetWord[t.Kind]+")")
				}
				return e
			}
			to, err := getList(ctx, tx, moveTo)
			if errors.Is(err, ErrNotFound) || err == nil && to.ID == id {
				return ErrMoveTo
			}
			if err != nil {
				return err
			}
			var routers []int64
			for _, t := range targets {
				moved = append(moved, t.Name)
				payload := map[string]any{"changes": "list", "from": l.Name, "to": to.Name}
				var subject events.Subject
				if t.Kind == "router" {
					if err := store.MoveRouter(ctx, tx, t.ID, to.ID); err != nil {
						return err
					}
					routers = append(routers, t.ID)
					subject = events.Subject{Type: "router", ID: strconv.FormatInt(t.ID, 10)}
					if err := s.record(ctx, tx, "routing.router_updated", subject, actor, payload); err != nil {
						return err
					}
					continue
				}
				if err := store.MoveShadowrocket(ctx, tx, t.ID, to.ID); err != nil {
					return err
				}
				subject = events.Subject{Type: "shadowrocket", ID: strconv.FormatInt(t.ID, 10)}
				if err := s.record(ctx, tx, "routing.shadowrocket_updated", subject, actor, payload); err != nil {
					return err
				}
			}
			if len(routers) > 0 {
				if err := s.d.Marker.Mark(ctx, tx, change.Change{Routers: routers, Actor: actor, Why: l.Name + " deleted: now " + to.Name}); err != nil {
					return err
				}
			}
		}
		if err := store.DeleteList(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.list_deleted", Subject(id), actor, map[string]any{"name": l.Name, "moved": strings.Join(moved, ", ")})
	})
}

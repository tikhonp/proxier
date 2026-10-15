package generations

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// HandOverSubscriber gives a removed member's generations, with their fetch
// URLs, to their heir (docs/processes/platform/members.md). A second
// delivery finds nothing left to move and records nothing.
func (s *Service) HandOverSubscriber() events.Subscriber {
	return events.Subscriber{
		Name:  "routerscripts.handover",
		Types: []string{"member.removed"},
		Handle: func(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
			h, ok := auth.HandoverOf(e)
			if !ok {
				return nil
			}
			n, err := store.HandOver(ctx, tx, h.Member, h.Heir)
			if err != nil || n == 0 {
				return err
			}
			_, err = s.d.Events.Record(ctx, tx, events.Event{
				Time: db.At(s.d.Now()), Type: "routerscript.handed_over", Subject: e.Subject, Actor: events.ActorSystem,
				Payload: map[string]any{"from": h.Username, "to": h.HeirUsername, "generations": n},
			})
			return err
		},
	}
}

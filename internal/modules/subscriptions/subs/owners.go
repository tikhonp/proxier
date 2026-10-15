package subs

import (
	"context"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// HandOverSubscriber gives a removed member's subscriptions and links to
// their heir, so links keep serving (docs/processes/platform/members.md).
// A second delivery finds nothing left to move and records nothing.
func (s *Service) HandOverSubscriber() events.Subscriber {
	return events.Subscriber{
		Name:  "subscriptions.handover",
		Types: []string{"member.removed"},
		Handle: func(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
			h, ok := auth.HandoverOf(e)
			if !ok {
				return nil
			}
			subs, links, err := store.HandOver(ctx, tx, h.Member, h.Heir)
			if err != nil || subs+links == 0 {
				return err
			}
			_, err = s.d.Events.Record(ctx, tx, events.Event{
				Time: s.now(), Type: "subscription.handed_over", Subject: e.Subject, Actor: events.ActorSystem,
				Payload: map[string]any{"from": h.Username, "to": h.HeirUsername, "subscriptions": subs, "links": links},
			})
			return err
		},
	}
}

// OwnedSubjects are the subjects of the subscriptions and links of member.
func (s *Service) OwnedSubjects(ctx context.Context, member int64) ([]events.Subject, error) {
	subIDs, linkIDs, err := store.Owned(ctx, s.d.DB.R, member)
	if err != nil {
		return nil, err
	}
	out := make([]events.Subject, 0, len(subIDs)+len(linkIDs))
	for _, id := range subIDs {
		out = append(out, Subject(id))
	}
	for _, id := range linkIDs {
		out = append(out, events.Subject{Type: "link", ID: strconv.FormatInt(id, 10)})
	}
	return out, nil
}

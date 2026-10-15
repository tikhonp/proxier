package routing

import (
	"context"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// Subscribers: a removed member's things go to their heir.
func (m *Module) Subscribers() []events.Subscriber {
	return []events.Subscriber{{
		Name:   "routing.handover",
		Types:  []string{"member.removed"},
		Handle: m.handOver,
	}}
}

// handOver gives a removed member's lists, routers, Shadowrocket configs,
// custom services, imports and discovery runs to their heir: nothing is
// deleted, so routers keep syncing and configs keep serving
// (docs/processes/platform/members.md). A second delivery finds nothing
// left to move and records nothing.
func (m *Module) handOver(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
	h, ok := auth.HandoverOf(e)
	if !ok {
		return nil
	}
	moved, err := store.HandOver(ctx, tx, h.Member, h.Heir)
	if err != nil {
		return err
	}
	payload := map[string]any{"from": h.Username, "to": h.HeirUsername}
	var total int64
	for _, k := range store.HandOverKinds() {
		payload[k] = moved[k]
		total += moved[k]
	}
	if total == 0 {
		return nil
	}
	_, err = m.deps.Events.Record(ctx, tx, events.Event{
		Time: db.At(m.Now()), Type: "routing.handed_over", Subject: e.Subject, Actor: events.ActorSystem, Payload: payload,
	})
	return err
}

// subjectTypes are the event subject types of what HandOver moves, by kind
// (imports have no events of their own).
var subjectTypes = map[string]string{
	"lists": "routing_list", "routers": "router", "shadowrocket": "shadowrocket", "services": "service", "discovery": "discovery",
}

// OwnedSubjects are the subjects of what member owns, for Activity and Jobs
// at activity: own.
func (m *Module) OwnedSubjects(ctx context.Context, member int64) ([]events.Subject, error) {
	owned, err := store.Owned(ctx, m.deps.DB.R, member)
	if err != nil {
		return nil, err
	}
	var out []events.Subject
	for _, k := range store.HandOverKinds() {
		typ, ok := subjectTypes[k]
		if !ok {
			continue
		}
		for _, id := range owned[k] {
			out = append(out, events.Subject{Type: typ, ID: strconv.FormatInt(id, 10)})
		}
	}
	return out, nil
}

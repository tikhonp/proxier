package notify

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// SubjectOwner is module.SubjectOwner: who owns the subject of an event.
type SubjectOwner interface {
	Owner(ctx context.Context, s events.Subject) (member int64, ok bool, err error)
}

// AddOwner registers a module whose things have owners.
func (s *Service) AddOwner(o SubjectOwner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owners = append(s.owners, o)
}

// inAudience reports whether a member with grant g, who owns the event's
// subject or not, may get a notification of type t
// (docs/processes/platform/notifications.md#recipients).
func inAudience(t events.Type, g access.Grant, owns bool) bool {
	switch t.Audience {
	case events.AudienceSubject:
		return owns
	case events.AudienceSettings:
		return g.Can(access.Settings, access.LevelManage)
	}
	return owns || managesArea(g, t.Module)
}

// managesArea: the members at the top of a module's area hear about all of
// it. A platform or servers event is about the shared machinery, which
// managing either Settings or the servers looks after.
func managesArea(g access.Grant, module string) bool {
	switch a := access.AreaOfModule(module); a {
	case access.Servers, access.Settings:
		return g.Can(access.Servers, access.LevelManage) || g.Can(access.Settings, access.LevelManage)
	default:
		return g.Can(a, a.Top())
	}
}

// mayReceive reports whether a member with grant g can ever get a
// notification of type t: whether its rule is theirs to choose. In an area
// where things have owners, any member who makes things there may own a
// subject.
func mayReceive(t events.Type, g access.Grant) bool {
	if t.Audience != events.AudienceArea {
		return inAudience(t, g, true)
	}
	a := access.AreaOfModule(t.Module)
	return managesArea(g, t.Module) || a.ManageLevel() == access.LevelOwn && g.Sees(a)
}

// recipients are the members an event's notification goes to: those in its
// audience who are in, have finished setup and have its rule on.
func (s *Service) recipients(ctx context.Context, tx *sqlx.Tx, e events.Event, t events.Type) ([]auth.Member, error) {
	var rows []struct {
		Member  int64 `db:"member_id"`
		Enabled bool  `db:"enabled"`
	}
	if err := tx.SelectContext(ctx, &rows, `SELECT member_id, enabled FROM notification_rules WHERE event_type = ?`, e.Type); err != nil {
		return nil, err
	}
	rules := make(map[int64]bool, len(rows))
	for _, r := range rows {
		rules[r.Member] = r.Enabled
	}
	owner, owned, err := s.owner(ctx, e.Subject)
	if err != nil {
		return nil, err
	}
	roster, err := s.a.Roster(ctx)
	if err != nil {
		return nil, err
	}
	var out []auth.Member
	for _, m := range roster {
		if m.Removed() || !m.SetUp() {
			continue
		}
		on, chosen := rules[m.ID]
		if !chosen {
			on = t.Notify
		}
		if on && inAudience(t, m.Grant, owned && owner == m.ID) {
			out = append(out, m.Member)
		}
	}
	return out, nil
}

// owner is the member who owns an event's subject: the member a member's
// events are about, or what a module says of its own things.
func (s *Service) owner(ctx context.Context, sub events.Subject) (int64, bool, error) {
	switch sub.Type {
	case "":
		return 0, false, nil
	case "member", "admin":
		id := auth.SubjectMember(sub)
		return id, id > 0, nil
	}
	s.mu.RLock()
	owners := append([]SubjectOwner(nil), s.owners...)
	s.mu.RUnlock()
	for _, o := range owners {
		id, ok, err := o.Owner(ctx, sub)
		if err != nil || ok {
			return id, ok, err
		}
	}
	return 0, false, nil
}

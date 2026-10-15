package pages

import (
	"context"

	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// own is what Activity and Jobs show the signed-in member at activity: own:
// the events and jobs they caused, and those whose subject is theirs (their
// own member subject, and what the modules say they own). nil at all: everyone's.
func (h *handler) own(ctx context.Context) (*events.Own, error) {
	w := web.Who(ctx)
	if _, ok := w.Scope(access.Activity).Member(); !ok {
		return nil, nil
	}
	o := &events.Own{Actors: events.MemberActors(w.Member), Subjects: []events.Subject{auth.MemberSubject(w.Member)}}
	if w.Member == 1 {
		o.Subjects = append(o.Subjects, events.Subject{Type: "admin", ID: "1"})
	}
	for _, m := range h.Owners {
		subs, err := m.OwnedSubjects(ctx, w.Member)
		if err != nil {
			return nil, err
		}
		o.Subjects = append(o.Subjects, subs...)
	}
	return o, nil
}

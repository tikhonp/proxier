package links

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
)

// Serve builds what the link gets now with ?format=query: the stub texts in
// the link's language, dates in the display zone, the members the catalog
// serves at this moment. Nothing is cached. It also returns the format
// picked; output.ErrBadFormat for a query the subscription doesn't allow.
func (s *Service) Serve(ctx context.Context, l Link, query string, masked bool) (output.Response, string, error) {
	sub, err := s.d.Subs.Get(ctx, l.SubscriptionID)
	if err != nil {
		return output.Response{}, "", err
	}
	format, err := output.PickFormat(query, sub.Formats, sub.DefaultFormat, l.Format)
	if err != nil {
		return output.Response{}, "", err
	}
	members, err := s.d.Subs.Members(ctx, sub.ID)
	if err != nil {
		return output.Response{}, "", err
	}
	var served []output.Server
	for _, m := range members {
		if m.InService {
			served = append(served, m.Server)
		}
	}
	contact, err := s.d.Settings.Get(ctx, "general.admin_contact")
	if err != nil {
		return output.Response{}, "", err
	}
	loc := s.d.I18n.Localizer(l.Lang, s.Zone(ctx))
	resp, err := output.Build(loc, output.Request{
		Link: output.Link{State: l.State, Expires: l.Expires}, Title: sub.Title, UpdateHours: sub.UpdateHours,
		Format: format, Servers: served, Hide: sub.Hide, Contact: contact, Now: s.d.Now(), Masked: masked,
	})
	return resp, format, err
}

package links

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// Served is what a link gets now, with what a page about it shows besides.
type Served struct {
	output.Response
	Format  string // the format picked
	Title   string // the subscription's
	Contact string // general.admin_contact
	// Loc is the link's language in the display zone.
	Loc *i18n.Localizer
}

// Serve builds what the link gets now with ?format=query: the stub texts in
// the link's language, dates in the display zone, the members the catalog
// serves at this moment. Nothing is cached. output.ErrBadFormat for a query
// the subscription doesn't allow.
func (s *Service) Serve(ctx context.Context, l Link, query string, masked bool) (Served, error) {
	// The link is already found; its subscription may be another member's.
	var sub subs.Subscription
	var served []output.Server
	if l.State == "deleted" && l.SubscriptionID == 0 {
		// A tombstone outlives its subscription. Accept every supported format
		// so an app can still refresh its list with the removal message.
		sub.Formats, sub.DefaultFormat, sub.UpdateHours = output.Names(), "uri-plain", 12
		sub.Title = l.Name
	} else {
		var err error
		sub, err = s.d.Subs.Get(ctx, l.SubscriptionID, access.All)
		if err != nil {
			return Served{}, err
		}
		members, err := s.d.Subs.Members(ctx, sub.ID, access.All)
		if err != nil {
			return Served{}, err
		}
		for _, m := range members {
			if m.InService {
				served = append(served, m.Server)
			}
		}
	}
	format, err := output.PickFormat(query, sub.Formats, sub.DefaultFormat, l.Format)
	if err != nil {
		return Served{}, err
	}
	contact, err := s.d.Settings.Get(ctx, "general.admin_contact")
	if err != nil {
		return Served{}, err
	}
	loc := s.d.I18n.Localizer(l.Lang, s.Zone(ctx))
	resp, err := output.Build(loc, output.Request{
		Link: output.Link{State: l.State, Expires: l.Expires}, Title: sub.Title, UpdateHours: sub.UpdateHours,
		Format: format, Servers: served, Hide: sub.Hide, Contact: contact, Now: s.d.Now(), Masked: masked, ScoreNames: sub.ScoreNames,
	})
	if err != nil {
		return Served{}, err
	}
	return Served{Response: resp, Format: format, Title: sub.Title, Contact: contact, Loc: loc}, nil
}

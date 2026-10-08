package subs

import (
	"context"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// Preview builds what an active link without expiry gets now, in the
// language and zone of ctx. Records nothing.
func (s *Service) Preview(ctx context.Context, id int64, format string, masked bool) (output.Response, []Member, error) {
	sub, err := s.Get(ctx, id)
	if err != nil {
		return output.Response{}, nil, err
	}
	f, err := output.PickFormat(format, sub.Formats, sub.DefaultFormat, "")
	if err != nil {
		return output.Response{}, nil, err
	}
	members, err := s.Members(ctx, id)
	if err != nil {
		return output.Response{}, nil, err
	}
	contact, err := s.d.Settings.Get(ctx, "general.admin_contact")
	if err != nil {
		return output.Response{}, nil, err
	}
	var served []output.Server
	for _, m := range members {
		if m.InService {
			served = append(served, m.Server)
		}
	}
	resp, err := output.Build(i18n.From(ctx), output.Request{
		Link: output.Link{State: "active"}, Title: sub.Title, UpdateHours: sub.UpdateHours, Format: f,
		Servers: served, Hide: sub.Hide, Contact: contact, Now: s.d.Now(), Masked: masked,
	})
	return resp, members, err
}

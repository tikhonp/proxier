package lists

import (
	"context"
	"strconv"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// Warning is a line of a list's Warnings area.
type Warning struct {
	Kind string // refused, guarded, rejected, failing
	At   time.Time
	Text string // rendered for the admin's language
	Href string
}

// refusalsShown bounds the guard refusals a list shows, of the last week.
const (
	refusalsShown = 5
	refusalsSince = 7 * 24 * time.Hour
)

// Warnings lists what needs the admin's eye on a list: the guard refusals of
// the last 7 days (newest first, at most 5), then the names left out now
// because they cover a server's hostname.
func (s *Service) Warnings(ctx context.Context, id int64) ([]Warning, error) {
	v, err := s.View(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	return s.warnings(ctx, v)
}

func (s *Service) warnings(ctx context.Context, v View) ([]Warning, error) {
	var out []Warning
	refused, err := events.List(ctx, s.d.DB.R, events.Filter{
		Type: "routing.list_refused_server_hostname", Subject: Subject(v.List.ID),
		Since: s.d.Now().Add(-refusalsSince), Limit: refusalsShown,
	})
	if err != nil {
		return nil, err
	}
	for _, e := range refused {
		args := i18n.Args{}
		for k, val := range e.Payload {
			args[k] = val
		}
		out = append(out, Warning{Kind: "refused", At: e.Time.Time, Text: i18n.T(ctx, "lists.warning.refused", args),
			Href: "/activity?subject=" + Subject(v.List.ID).String()})
	}
	for _, m := range v.Members {
		w, err := s.refreshWarning(ctx, m.Service.ID)
		if err != nil {
			return nil, err
		}
		if w != nil {
			out = append(out, *w)
		}
	}
	for _, m := range v.Members {
		for _, d := range m.Owned.Dropped {
			if d.Reason != own.ReasonGuarded {
				continue
			}
			src := m.Service.Selector
			if src == "" {
				src = m.Service.Tag
			}
			out = append(out, Warning{Kind: "guarded", Text: i18n.T(ctx, "lists.warning.guarded", i18n.Args{
				"source": src, "domain": d.Name, "hostname": d.Via, "server": d.By,
			}), Href: "/routing/services/" + strconv.FormatInt(m.Service.ID, 10)})
		}
	}
	return out, nil
}

// refreshWarning is a member's waiting snapshot ("Rejected snapshot · …") or
// its failing refresh, nil when neither.
func (s *Service) refreshWarning(ctx context.Context, serviceID int64) (*Warning, error) {
	svc, err := store.GetService(ctx, s.d.DB.R, serviceID)
	if err != nil || svc.Source == "custom" {
		return nil, err
	}
	href := "/routing/services/" + strconv.FormatInt(serviceID, 10)
	w, waiting, err := store.Waiting(ctx, s.d.DB.R, serviceID)
	if err != nil {
		return nil, err
	}
	if waiting {
		acc, err := store.AcceptedSnapshot(ctx, s.d.DB.R, serviceID)
		if err != nil {
			return nil, err
		}
		old := int64(acc.SuffixCount + acc.ExactCount)
		args := i18n.Args{"source": svc.Selector, "old": i18n.From(ctx).Number(old), "new": i18n.From(ctx).Number(int64(w.SuffixCount + w.ExactCount)), "pct": w.LostPct}
		key := "lists.warning.rejected"
		if w.Reason == "empty" {
			key = "lists.warning.rejected_empty"
		}
		return &Warning{Kind: "rejected", At: w.FetchedAt.Time, Text: i18n.N(ctx, key, old, args), Href: href}, nil
	}
	if svc.Failures > 0 {
		return &Warning{Kind: "failing", Text: i18n.N(ctx, "lists.warning.failing", int64(svc.Failures), i18n.Args{
			"source": svc.Selector, "error": svc.LastError,
		}), Href: href}, nil
	}
	return nil, nil
}

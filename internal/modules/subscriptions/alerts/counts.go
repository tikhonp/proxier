package alerts

import (
	"context"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Count is one network or app of a link's fetches.
type Count struct {
	Key     string // a network, or an app family / "ua:<user agent>"
	Country string // networks only; "" unknown
	Fetches int
}

// Counts are the networks and apps that fetched a link, most fetches first.
type Counts struct{ Networks, Apps []Count }

// Counts lists who fetched a link after since. Every outcome counts, stubs
// too; an unknown app counts by its user agent.
func (s *Service) Counts(ctx context.Context, linkID int64, since time.Time) (Counts, error) {
	nets, err := store.FetchNetworks(ctx, s.d.DB.R, linkID, db.At(since))
	if err != nil {
		return Counts{}, err
	}
	apps, err := store.FetchApps(ctx, s.d.DB.R, linkID, db.At(since))
	if err != nil {
		return Counts{}, err
	}
	conv := func(gs []store.Group) []Count {
		out := make([]Count, len(gs))
		for i, g := range gs {
			out[i] = Count{Key: g.Key, Country: g.Country, Fetches: g.Fetches}
		}
		return out
	}
	return Counts{Networks: conv(nets), Apps: conv(apps)}, nil
}

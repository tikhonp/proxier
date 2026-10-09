package lists

import (
	"context"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// Target is a router or a Shadowrocket config following a list.
type Target struct {
	Kind  string // router, shadowrocket
	ID    int64
	Name  string
	State string // routers: their state (3e words it); configs: enabled or disabled
	// LastFetch is a Shadowrocket config's last fetch; zero when it was never
	// fetched, and for routers.
	LastFetch time.Time
}

// Targets reads the routers and Shadowrocket configs following a list:
// routers first, each by name.
func (s *Service) Targets(ctx context.Context, id int64) ([]Target, error) {
	rows, err := store.ListTargets(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		out = append(out, Target{Kind: r.Kind, ID: r.ID, Name: r.Name, State: r.State, LastFetch: r.LastFetch.Time})
	}
	return out, nil
}

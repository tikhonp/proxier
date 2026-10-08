package refresh

import (
	"context"
	"errors"
	"slices"

	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// roundPayload is what the round keeps between its steps and across a
// restart: the services it has refreshed, and the tags that failed with
// whether they started failing in this round.
type roundPayload struct {
	Done     []int64         `json:"done,omitempty"`
	Failed   map[string]bool `json:"failed,omitempty"`
	Services int             `json:"services"`
}

// runRound refreshes every upstream service in at least one list, by id, each
// in its own transaction. A failing one doesn't stop the others. After each
// service the payload is saved, so a resume skips what is done.
func (s *Service) runRound(ctx context.Context, r *jobs.Run) error {
	var p roundPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	if p.Failed == nil {
		p.Failed = map[string]bool{}
	}
	ids, err := store.UpstreamInLists(ctx, s.d.DB.R)
	if err != nil {
		return err
	}
	p.Services = len(ids)
	for _, id := range ids {
		if slices.Contains(p.Done, id) {
			continue
		}
		out, err := s.Refresh(ctx, id, true, r.Info().Actor())
		switch {
		case errors.Is(err, services.ErrNotFound) || errors.Is(err, services.ErrCustom):
		case err != nil:
			return err
		default:
			logOutcome(r, out)
			if out.Result == Failed {
				cur, err := store.GetService(ctx, s.d.DB.R, id)
				if err != nil {
					return err
				}
				p.Failed[out.Tag] = cur.Failures == 1
			}
		}
		p.Done = append(p.Done, id)
		if err := r.SavePayload(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

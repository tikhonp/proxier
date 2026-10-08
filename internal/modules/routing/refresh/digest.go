package refresh

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Digest is a round's summary, the payload of routing.refresh_digest.
type Digest struct {
	Changed, Added, Removed int
	Rejected                int
	Failing, StillFailing   int // started failing in this round; failing since before
	Services                int
}

// Payload is the event's payload.
func (d Digest) Payload() map[string]any {
	return map[string]any{
		"changed": d.Changed, "added": d.Added, "removed": d.Removed, "rejected": d.Rejected,
		"failing": d.Failing, "still_failing": d.StillFailing, "services": d.Services,
	}
}

// runDigest records the round's digest from its own events and its payload.
// It notifies only with news (the event's NotifyIf).
func (s *Service) runDigest(ctx context.Context, r *jobs.Run) error {
	var p roundPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	actor := r.Info().Actor()
	d := Digest{Services: p.Services}
	for _, f := range p.Failed {
		if f {
			d.Failing++
		} else {
			d.StillFailing++
		}
	}
	acc, err := events.List(ctx, s.d.DB.R, events.Filter{Type: "routing.snapshot_accepted", Actor: actor, Limit: 100000})
	if err != nil {
		return err
	}
	for _, e := range acc {
		d.Changed++
		d.Added += intOf(e.Payload["added"])
		d.Removed += intOf(e.Payload["removed"])
	}
	rej, err := events.List(ctx, s.d.DB.R, events.Filter{Type: "routing.snapshot_rejected", Actor: actor, Limit: 100000})
	if err != nil {
		return err
	}
	d.Rejected = len(rej)
	r.Log().Info("%d services: %d changed (+%d −%d), %d held back, %d started failing, %d still failing",
		d.Services, d.Changed, d.Added, d.Removed, d.Rejected, d.Failing, d.StillFailing)
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return s.record(ctx, tx, "routing.refresh_digest", RoundSubject, actor, d.Payload())
	})
}

// intOf reads a number payload field: an int when recorded, a float64 once
// read back from JSON.
func intOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

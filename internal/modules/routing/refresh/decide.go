package refresh

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// decide handles a refresh that got an answer, inside its transaction: the
// same set is bookkeeping (and ends a waiting rejection); a different one is
// accepted, or held back by the safety checks.
func (s *Service) decide(ctx context.Context, tx *sqlx.Tx, cur store.Service, res sources.Resolved, inRound bool,
	minNames, maxPct int, actor string, out *Outcome) error {
	now := db.At(s.d.Now())
	if err := store.RefreshAnswered(ctx, tx, cur.ID, now); err != nil {
		return err
	}
	acc, err := store.AcceptedSnapshot(ctx, tx, cur.ID)
	if err != nil {
		return err
	}
	waiting, isWaiting, err := store.Waiting(ctx, tx, cur.ID)
	if err != nil {
		return err
	}
	if res.Set.Hash() == acc.Hash {
		out.Result = Same
		if !isWaiting {
			return nil
		}
		// upstream came back: what was held back is moot
		if err := store.DismissSnapshot(ctx, tx, waiting.ID, now); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.snapshot_dismissed", services.Subject(cur.ID), actor, map[string]any{
			"new_count": waiting.SuffixCount + waiting.ExactCount, "automatic": true,
		})
	}
	old := snapshot.Set{Suffix: snapshot.Decode(acc.Suffix), Exact: snapshot.Decode(acc.Exact)}
	d := snapshot.Compare(old, res.Set)
	out.Added, out.Removed = d.Added(), d.Removed()
	row, err := newRow(cur, res, d, inRound, now)
	if err != nil {
		return err
	}
	reason, lost := Check(old, res.Set, minNames, maxPct)
	if reason != "" {
		out.Result = Rejected
		if isWaiting && waiting.Hash == row.Hash {
			return nil // the same rejection again: one is enough
		}
		row.Status, row.Reason, row.LostPct, row.AcceptedAt = "rejected", reason, lost, db.Time{}
		if _, err := store.InsertSnapshot(ctx, tx, row); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.snapshot_rejected", services.Subject(cur.ID), actor, map[string]any{
			"reason": reason, "old_count": acc.SuffixCount + acc.ExactCount, "new_count": res.Set.Count(),
			"lost_pct": lost, "in_round": inRound,
		})
	}
	out.Result = Accepted
	if _, err := store.Accept(ctx, tx, row); err != nil {
		return err
	}
	if err := s.record(ctx, tx, "routing.snapshot_accepted", services.Subject(cur.ID), actor, map[string]any{
		"added": d.Added(), "removed": d.Removed(), "suffix": len(res.Set.Suffix), "exact": len(res.Set.Exact),
		"forced": false, "in_round": inRound,
	}); err != nil {
		return err
	}
	return s.d.Marker.Mark(ctx, tx, change.Change{Services: []int64{cur.ID}, Actor: actor, Why: cur.Tag + " changed upstream"})
}

func newRow(cur store.Service, res sources.Resolved, d snapshot.Diff, inRound bool, now db.Time) (store.Snapshot, error) {
	skipped := []byte("[]")
	if len(res.Set.Skipped) > 0 {
		var err error
		if skipped, err = json.Marshal(res.Set.Skipped); err != nil {
			return store.Snapshot{}, err
		}
	}
	return store.Snapshot{
		ServiceID: cur.ID, Status: "accepted", Selector: cur.Selector, Portal: res.Portal, Kind: res.Kind,
		Suffix: snapshot.Encode(res.Set.Suffix), Exact: snapshot.Encode(res.Set.Exact), Skipped: string(skipped),
		SuffixCount: len(res.Set.Suffix), ExactCount: len(res.Set.Exact), Hash: res.Set.Hash(),
		Added: d.Added(), Removed: d.Removed(), InRound: inRound, FetchedAt: now, AcceptedAt: now,
	}, nil
}

// Accept makes the waiting snapshot the accepted one (Accept anyway): its
// added and removed are counted again against the snapshot it replaces, and
// every list holding the service is marked.
func (s *Service) Accept(ctx context.Context, serviceID, snapshotID int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, w, err := s.waitingIn(ctx, tx, serviceID, snapshotID)
		if err != nil {
			return err
		}
		acc, err := store.AcceptedSnapshot(ctx, tx, serviceID)
		if err != nil {
			return err
		}
		old := snapshot.Set{Suffix: snapshot.Decode(acc.Suffix), Exact: snapshot.Decode(acc.Exact)}
		set := snapshot.Set{Suffix: snapshot.Decode(w.Suffix), Exact: snapshot.Decode(w.Exact)}
		d := snapshot.Compare(old, set)
		if err := store.AcceptRejected(ctx, tx, serviceID, w.ID, actor, d.Added(), d.Removed(), db.At(s.d.Now())); err != nil {
			return err
		}
		if err := s.record(ctx, tx, "routing.snapshot_accepted", services.Subject(serviceID), actor, map[string]any{
			"added": d.Added(), "removed": d.Removed(), "suffix": w.SuffixCount, "exact": w.ExactCount,
			"forced": true, "in_round": false,
		}); err != nil {
			return err
		}
		return s.d.Marker.Mark(ctx, tx, change.Change{Services: []int64{serviceID}, Actor: actor, Why: cur.Tag + " accepted anyway"})
	})
}

// Dismiss leaves the waiting snapshot rejected: the next refresh checks again
// from the accepted one.
func (s *Service) Dismiss(ctx context.Context, serviceID, snapshotID int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, w, err := s.waitingIn(ctx, tx, serviceID, snapshotID)
		if err != nil {
			return err
		}
		if err := store.DismissSnapshot(ctx, tx, w.ID, db.At(s.d.Now())); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.snapshot_dismissed", services.Subject(serviceID), actor, map[string]any{
			"new_count": w.SuffixCount + w.ExactCount, "automatic": false,
		})
	})
}

func (s *Service) waitingIn(ctx context.Context, tx *sqlx.Tx, serviceID, snapshotID int64) (store.Service, store.Snapshot, error) {
	cur, err := store.GetService(ctx, tx, serviceID)
	if errors.Is(err, store.ErrNotFound) {
		return cur, store.Snapshot{}, services.ErrNotFound
	}
	if err != nil {
		return cur, store.Snapshot{}, err
	}
	w, ok, err := store.Waiting(ctx, tx, serviceID)
	if err != nil {
		return cur, w, err
	}
	if !ok || w.ID != snapshotID {
		return cur, w, ErrNotWaiting
	}
	return cur, w, nil
}

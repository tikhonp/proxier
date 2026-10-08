// Package refresh keeps upstream services current: the daily round, Refresh
// now and Refresh all, the safety checks, Accept anyway and Dismiss, and the
// round's digest (docs/processes/routing/upstream-refresh.md).
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// ErrNotWaiting: Accept anyway and Dismiss act only on the waiting snapshot.
var ErrNotWaiting = errors.New("refresh: the snapshot isn't waiting for a decision")

// The results of one refresh.
const (
	Same     = "same"
	Accepted = "accepted"
	Rejected = "rejected"
	Failed   = "failed"
)

// FailingAfter is how many failed refreshes in a row notify.
const FailingAfter = 3

// Outcome is what one refresh did.
type Outcome struct {
	ServiceID      int64
	Tag            string
	Result         string // same, accepted, rejected, failed
	Added, Removed int
	Err            error // failed: the resolve error
}

// Deps are what the service uses.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Settings *settings.Store
	Jobs     *jobs.System
	Services *services.Service
	Lists    *lists.Service
	Resolver *sources.Resolver // the jobs' resolver: 30 s per request
	Marker   change.Marker
	Now      func() time.Time
	Log      *slog.Logger
}

// Service refreshes upstream services.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Marker == nil {
		d.Marker = change.None{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, subject events.Subject, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: db.At(s.d.Now()), Type: typ, Subject: subject, Actor: actor, Payload: payload})
	return err
}

// Refresh fetches a service's selector again and decides on the result:
// same, accepted, rejected (held back by a safety check) or failed. A resolve
// error is an outcome, not an error: the error is the database's, or the
// context's when the caller gave up (nothing is recorded then).
func (s *Service) Refresh(ctx context.Context, serviceID int64, inRound bool, actor string) (Outcome, error) {
	it, err := s.d.Services.Get(ctx, serviceID)
	if err != nil {
		return Outcome{}, err
	}
	if it.Source == selector.Custom {
		return Outcome{}, services.ErrCustom
	}
	out := Outcome{ServiceID: it.ID, Tag: it.Tag}
	acc, err := store.AcceptedSnapshot(ctx, s.d.DB.R, it.ID)
	if err != nil {
		return out, err
	}
	sel, err := selector.Parse(it.Selector)
	if err != nil {
		return out, fmt.Errorf("refresh: %s: stored selector %q: %w", it.Tag, it.Selector, err)
	}
	// An unpinned iplist selector stays on the portal it was found on, so a
	// refresh never switches portals silently: a miss there is a failure.
	if sel.Source == selector.Iplist && sel.Portal == "" {
		sel.Portal = acc.Portal
		if sel.Portal == "" {
			sel.Portal = selector.Portals[0]
		}
	}
	res, rerr := s.d.Resolver.Resolve(ctx, sel)
	if rerr != nil && ctx.Err() != nil {
		return out, ctx.Err()
	}
	minNames, err := s.d.Settings.GetInt(ctx, conf.ShrinkMin)
	if err != nil {
		return out, err
	}
	maxPct, err := s.d.Settings.GetInt(ctx, conf.ShrinkPct)
	if err != nil {
		return out, err
	}
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetService(ctx, tx, it.ID)
		if errors.Is(err, store.ErrNotFound) {
			return services.ErrNotFound
		}
		if err != nil {
			return err
		}
		if rerr != nil {
			out.Result, out.Err = Failed, rerr
			return s.failed(ctx, tx, cur, rerr, actor)
		}
		return s.decide(ctx, tx, cur, res, inRound, int(minNames), int(maxPct), actor, &out)
	})
	return out, err
}

// failed counts a failure; the third in a row notifies, once per run.
func (s *Service) failed(ctx context.Context, tx *sqlx.Tx, cur store.Service, rerr error, actor string) error {
	text := sources.ErrorText(rerr)
	now, err := store.RefreshFailed(ctx, tx, cur.ID, text)
	if err != nil {
		return err
	}
	if now.Failures < FailingAfter || now.FailingNotified {
		return nil
	}
	if err := store.SetFailingNotified(ctx, tx, cur.ID); err != nil {
		return err
	}
	return s.record(ctx, tx, "routing.refresh_failing", services.Subject(cur.ID), actor, map[string]any{
		"failures": now.Failures, "error": text,
	})
}

// Waiting reads a service's waiting snapshot, with its names.
func (s *Service) Waiting(ctx context.Context, serviceID int64) (services.Snapshot, bool, error) {
	r, ok, err := store.Waiting(ctx, s.d.DB.R, serviceID)
	if err != nil || !ok {
		return services.Snapshot{}, false, err
	}
	sn, err := services.FromRow(r)
	return sn, err == nil, err
}

// State is a service's state: waiting, failing or ok.
func (s *Service) State(ctx context.Context, serviceID int64) (string, error) {
	all, err := store.States(ctx, s.d.DB.R)
	if err != nil {
		return "", err
	}
	if st, ok := all[serviceID]; ok {
		return st, nil
	}
	return "", services.ErrNotFound
}

// States reads every service's state.
func (s *Service) States(ctx context.Context) (map[int64]string, error) {
	return store.States(ctx, s.d.DB.R)
}

// LastDigest is the newest refresh_digest event (the dashboard, 3f).
func (s *Service) LastDigest(ctx context.Context) (events.Event, bool, error) {
	list, err := events.List(ctx, s.d.DB.R, events.Filter{Type: "routing.refresh_digest", Limit: 1})
	if err != nil || len(list) == 0 {
		return events.Event{}, false, err
	}
	return list[0], true, nil
}

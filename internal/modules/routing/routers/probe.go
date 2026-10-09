package routers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// ProbeEvery is how often an awaiting router is tried.
const ProbeEvery = 10 * time.Minute

type routerPayload struct {
	RouterID int64 `json:"router_id"`
}

// ProbeTarget is how a probe connects: the router's own key is pinned on
// first contact (nobody can confirm a fresh router's key), its jump host must
// already be confirmed.
func (c Connection) ProbeTarget(routerID int64) sshx.Target {
	t := c.Target(routerID)
	t.FirstContact = sshx.PinOnFirstContact
	return t
}

// probeRound queues one probe per awaiting router still probed; one busy
// with another job is skipped.
func (s *Service) probeRound(ctx context.Context, r *jobs.Run) error {
	ids, err := store.Probeable(ctx, s.d.DB.R, s.now())
	if err != nil {
		return err
	}
	return s.fanOut(ctx, r, ids, JobProbe)
}

// fanOut queues a job of typ per router, skipping busy ones.
func (s *Service) fanOut(ctx context.Context, r *jobs.Run, ids []int64, typ string) error {
	queued := 0
	for _, id := range ids {
		err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			_, err := s.d.Jobs.Enqueue(ctx, tx, jobs.Request{
				Type: typ, Payload: routerPayload{RouterID: id}, ResourceKey: resourceKey(id), SkipIfBusy: true,
				Subject: Subject(id), CreatedBy: r.Info().Actor(),
			})
			return err
		})
		switch {
		case errors.Is(err, jobs.ErrSkipped):
			r.Log().Info("Router %d is busy: skipped.", id)
		case err != nil:
			return err
		default:
			queued++
		}
	}
	r.Log().Info("%d of %d routers queued.", queued, len(ids))
	if queued > 0 {
		s.d.Jobs.Kick()
	}
	return nil
}

// stepProbe tries an awaiting router once. Success activates it, records
// router_connected and queues the initial sync; a failure records nothing
// (the job's error is what the page shows).
func (s *Service) stepProbe(ctx context.Context, r *jobs.Run) error {
	var p routerPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	rt, err := store.GetRouter(ctx, s.d.DB.R, p.RouterID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if rt.State != StateAwaiting || !rt.AwaitingUntil.After(s.d.Now()) {
		r.Log().Info("%s is no longer probed.", rt.Name)
		return nil
	}
	c := connOf(rt)
	cl, err := s.d.SSH.Connect(ctx, c.ProbeTarget(rt.ID), r.Log())
	if err != nil {
		return jobs.Permanent(fmt.Errorf("%s", ProblemOf(c, err).Text))
	}
	defer func() { _ = cl.Close() }()
	res, err := cl.Run(ctx, routeros.CmdCheck(c.Names), sshx.RunOptions{Timeout: time.Minute})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("the check exited with %d", res.ExitCode)
	}
	var chk routeros.Check
	if err == nil {
		chk, err = routeros.ParseCheck(res.Stdout)
	}
	if err != nil {
		return jobs.Permanent(fmt.Errorf("the router's check failed: %w", err))
	}
	r.Log().Info("Connected to %s: RouterOS %s on %s.", rt.Name, chk.Version, chk.Board)
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return s.activate(ctx, tx, rt.ID, chk.Version, chk.Board, r.Info().Actor())
	})
	if err == nil {
		s.d.Jobs.Kick()
	}
	return err
}

// activate ends a router's awaiting setup: active, connected, its initial
// sync queued. A router no longer awaiting is left alone.
func (s *Service) activate(ctx context.Context, tx *sqlx.Tx, id int64, version, board, actor string) error {
	cur, err := store.GetRouter(ctx, tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if cur.State != StateAwaiting {
		return nil
	}
	now := s.now()
	if err := store.RouterActivated(ctx, tx, id, version, board, now); err != nil {
		return err
	}
	return s.connected(ctx, tx, id, version, board, now, actor, true)
}

// quietFailure records nothing when a job of a read-only check fails: the
// job's error is enough.
func quietFailure(context.Context, *sqlx.Tx, jobs.Info, error) error { return nil }

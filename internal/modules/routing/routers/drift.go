package routers

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// DriftEvery is the drift round's default interval (routing.drift_every).
const DriftEvery = 6 * time.Hour

// Drift is pure: the applied tags the router no longer holds as applied,
// sorted. A tag drifts when it is gone, when its DNS entries' (name, exact)
// differ from the applied hash, when an entry isn't FWD to the forwarder, or
// when its address-list count differs from the applied one. It compares with
// what Proxier applied, never with the desired state: pending changes have
// syncs of their own.
func Drift(st State, applied map[string]Applied, n routeros.Names) []string {
	var out []string
	for tag, a := range applied {
		t := st.Tags[tag]
		if t.empty() {
			out = append(out, tag)
			continue
		}
		es := make([]routeros.Entry, 0, len(t.DNS))
		fwd := true
		for _, d := range t.DNS {
			es = append(es, routeros.Entry{Name: d.Name, Exact: !d.Subdomain})
			if d.Type != "FWD" || d.ForwardTo != n.Forwarder {
				fwd = false
			}
		}
		if !fwd || routeros.EntriesHash(es) != a.Hash || len(t.List) != a.Suffix+a.Exact {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// driftRows are a drift check's rows: per drifted tag, what was applied
// (Want) and the DNS entries on the router now (Have).
func driftRows(st State, applied map[string]Applied, tags []string) []TagPlan {
	out := make([]TagPlan, 0, len(tags))
	for _, tag := range tags {
		a := applied[tag]
		out = append(out, TagPlan{Tag: tag, Want: a.Suffix + a.Exact, Have: st.Tags[tag].Entries(), Action: Update, Why: WhyDrift})
	}
	return out
}

// driftRound queues a drift check of every active router that has connected;
// one with a sync (or anything else) queued or running is skipped: that job
// reads it anyway.
func (s *Service) driftRound(ctx context.Context, r *jobs.Run) error {
	ids, err := store.DriftCheckable(ctx, s.d.DB.R)
	if err != nil {
		return err
	}
	return s.fanOut(ctx, r, ids, JobDrift)
}

// stepDrift reads the router and compares it with what was applied. Drift
// with routing.drift_repair on queues a sync now; with it off, it notifies
// once per set of drifted tags. A check that can't read the router fails its
// row only: no failure is counted and nothing is recorded.
func (s *Service) stepDrift(ctx context.Context, r *jobs.Run) error {
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
	if rt.State != StateActive {
		r.Log().Info("%s is %s: nothing to check.", rt.Name, rt.State)
		return nil
	}
	ctx, done := cancellable(ctx, r)
	defer done()
	syncID, err := s.syncRow(ctx, r, rt, syncPayload{}, "drift")
	if err != nil {
		return err
	}
	cl, err := s.connect(ctx, r, rt)
	if err != nil {
		return s.readFailed(ctx, rt, syncID, StepConnect, err)
	}
	defer func() { _ = cl.Close() }()
	names := connOf(rt).Names
	st, err := s.read(ctx, cl, names)
	if err != nil {
		return s.readFailed(ctx, rt, syncID, StepRead, err)
	}
	applied, err := s.Applied(ctx, rt.ID)
	if err != nil {
		return err
	}
	plan, _, err := s.plan(ctx, rt, st, Extra{})
	if err != nil {
		return err
	}
	drift := Drift(st, applied, names)
	repair, err := s.d.Settings.GetBool(ctx, conf.DriftRepair)
	if err != nil {
		return err
	}
	if len(drift) == 0 {
		r.Log().Info("No drift: the router holds what Proxier installed.")
	} else {
		r.Log().Info("Drift: %s.", strings.Join(drift, ", "))
	}
	actor := r.Info().Actor()
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		at := db.At(st.ReadAt)
		joined := strings.Join(drift, ",")
		if err := store.SyncPlanned(ctx, tx, syncID, jsonOf(driftRows(st, applied, drift)), at, store.Sync{}); err != nil {
			return err
		}
		if err := store.SetSyncDrift(ctx, tx, syncID, joined); err != nil {
			return err
		}
		if err := store.SyncDone(ctx, tx, syncID, "", s.now()); err != nil {
			return err
		}
		if err := store.RouterRead(ctx, tx, rt.ID, st.Untagged, len(st.Pins), at); err != nil {
			return err
		}
		if err := s.recordUnmanaged(ctx, tx, rt, st, UnmanagedTags(st, applied, desiredTags(plan)), actor); err != nil {
			return err
		}
		if err := store.SetRouterDrift(ctx, tx, rt.ID, joined, at); err != nil {
			return err
		}
		switch {
		case len(drift) == 0:
			return nil
		case repair:
			if _, err := s.enqueueSync(ctx, tx, rt.ID, syncPayload{Trigger: TriggerDrift, Why: []string{"drift: " + strings.Join(drift, ", ")}}, 0, actor); err != nil {
				return err
			}
			return s.record(ctx, tx, "routing.drift_detected", rt.ID, actor, map[string]any{"tags": strings.Join(drift, ", "), "repair": true})
		case joined != rt.Drift:
			return s.record(ctx, tx, "routing.drift_detected", rt.ID, actor, map[string]any{"tags": strings.Join(drift, ", "), "repair": false})
		}
		return nil
	})
	if err == nil && len(drift) > 0 && repair {
		s.d.Jobs.Kick()
	}
	return err
}

// readFailed ends a read-only job's row (a drift check) failed at a step and
// fails the job without a retry; nothing else is recorded.
func (s *Service) readFailed(ctx context.Context, rt store.Router, syncID int64, step string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	pr := ProblemOf(connOf(rt), err)
	f := &failure{step: step, hop: pr.Hop, text: pr.Text + Untouched, err: err}
	if step != StepConnect {
		f.hop, f.text = "", "Reading the router failed: "+err.Error()+"."+Untouched
	}
	werr := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return store.SyncFailed(ctx, tx, syncID, f.step, f.hop, f.text, s.now())
	})
	return errors.Join(werr, jobs.Permanent(f))
}

// desiredTags are the plan's desired tags (owning names or not).
func desiredTags(plan []TagPlan) map[string]bool {
	out := map[string]bool{}
	for _, p := range plan {
		if p.Want >= 0 {
			out[p.Tag] = true
		}
	}
	return out
}

// Repair queues a sync now for drift (routing.drift_repair off).
func (s *Service) Repair(ctx context.Context, id int64, actor string) (int64, error) {
	return s.syncAtOnce(ctx, id, syncPayload{Trigger: TriggerDrift, Why: []string{"repair"}}, actor)
}

// Schedules: the drift round every routing.drift_every, the probe round
// every 10 minutes.
func (s *Service) Schedules() []jobs.Schedule {
	return []jobs.Schedule{
		{Name: JobDriftRound, Every: DriftEvery, Setting: conf.DriftEvery, Jitter: 10 * time.Minute,
			Request: func(context.Context) (jobs.Request, error) {
				return jobs.Request{Type: JobDriftRound, CoalescingKey: "round"}, nil
			}},
		{Name: JobProbeRound, Every: ProbeEvery,
			Request: func(context.Context) (jobs.Request, error) {
				return jobs.Request{Type: JobProbeRound, CoalescingKey: "round"}, nil
			}},
	}
}

// LatestDrift is a router's newest drift check; ok is false when it has none.
func (s *Service) LatestDrift(ctx context.Context, id int64) (Sync, bool, error) {
	r, err := store.LatestSync(ctx, s.d.DB.R, id, "drift")
	if errors.Is(err, store.ErrNotFound) {
		return Sync{}, false, nil
	} else if err != nil {
		return Sync{}, false, err
	}
	return syncOf(r), true, nil
}

package routers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// Sync steps (a failure's step) and the backoff between attempts.
const (
	StepConnect = "connect"
	StepRead    = "read"
	StepPush    = "push"
	StepVerify  = "verify"
)

var syncBackoff = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour}

// readTimeout bounds one read command.
const readTimeout = 2 * time.Minute

// JobTypes are routing.sync, routing.preview and routing.test.
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{
		{
			Name: JobSync, Queue: jobs.Routers, MaxAttempts: 4, Backoff: syncBackoff, Merge: mergeSync,
			Steps:    []jobs.Step{{Name: "connect", Run: s.stepConnect}, {Name: "sync", Run: s.stepSync}},
			OnFailed: s.syncJobFailed, OnCancelled: s.syncJobCancelled,
		},
		{
			Name: JobPreview, Queue: jobs.Routers, MaxAttempts: 1, Merge: keepQueued,
			Steps:    []jobs.Step{{Name: "read", Run: s.stepPreview}},
			OnFailed: s.syncJobFailed, OnCancelled: s.syncJobCancelled,
		},
		{
			Name: JobTest, Queue: jobs.Routers, MaxAttempts: 1,
			Steps:    []jobs.Step{{Name: "test", Run: s.stepTest}},
			OnFailed: s.testFailed, OnCancelled: s.testCancelled,
		},
	}
}

// failure is a failed phase of a sync, as the router page and the event say it.
type failure struct {
	step, hop, text string
	err             error
}

func (f *failure) Error() string { return f.step + ": " + f.text }
func (f *failure) Unwrap() error { return f.err }

// stepConnect connects and reads the check: a router no longer active (or
// gone) ends the job quietly.
func (s *Service) stepConnect(ctx context.Context, r *jobs.Run) error {
	p, rt, ok, err := s.loadSync(ctx, r)
	if err != nil || !ok {
		return err
	}
	ctx, done := cancellable(ctx, r)
	defer done()
	syncID, err := s.syncRow(ctx, r, rt, p, "sync")
	if err != nil {
		return err
	}
	cl, err := s.connect(ctx, r, rt)
	if err != nil {
		return s.fail(ctx, r, rt, syncID, p, err)
	}
	defer func() { _ = cl.Close() }()
	res, err := cl.Run(ctx, routeros.CmdCheck(connOf(rt).Names), sshx.RunOptions{Timeout: time.Minute})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("the check exited with %d", res.ExitCode)
	}
	var chk routeros.Check
	if err == nil {
		chk, err = routeros.ParseCheck(res.Stdout)
	}
	if err != nil {
		return s.fail(ctx, r, rt, syncID, p, &failure{step: StepConnect, hop: "router",
			text: "The router's check failed: " + err.Error() + "." + Untouched, err: err})
	}
	r.Log().Info("Connected to %s: RouterOS %s on %s.", rt.Name, chk.Version, chk.Board)
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		if err := store.RouterSeen(ctx, tx, rt.ID, chk.Version, chk.Board, now); err != nil {
			return err
		}
		if rt.ConnectedAt.IsZero() {
			return s.connected(ctx, tx, rt.ID, chk.Version, chk.Board, now, r.Info().Actor(), false)
		}
		return nil
	})
}

// stepSync reads, plans, pushes and verifies. It always starts from the
// read, also on a retry and after a restart, so it never pushes a stale plan.
func (s *Service) stepSync(ctx context.Context, r *jobs.Run) error {
	p, rt, ok, err := s.loadSync(ctx, r)
	if err != nil || !ok {
		return err
	}
	ctx, done := cancellable(ctx, r)
	defer done()
	log := r.Log()
	syncID, err := s.syncRow(ctx, r, rt, p, "sync")
	if err != nil {
		return err
	}
	cl, err := s.connect(ctx, r, rt)
	if err != nil {
		return s.fail(ctx, r, rt, syncID, p, err)
	}
	defer func() { _ = cl.Close() }()
	job := strconv.FormatInt(r.Info().ID, 10)
	// files a run cut short left behind
	if _, err := cl.Run(ctx, routeros.CmdRemoveJobFiles(job), sshx.RunOptions{Timeout: time.Minute}); err != nil {
		return s.fail(ctx, r, rt, syncID, p, &failure{step: StepRead, text: "Removing leftover files failed: " + err.Error() + "." + Untouched, err: err})
	}
	names := connOf(rt).Names
	st, err := s.read(ctx, cl, names)
	if err != nil {
		return s.fail(ctx, r, rt, syncID, p, &failure{step: StepRead, text: "Reading the router failed: " + err.Error() + "." + Untouched, err: err})
	}
	plan, view, err := s.plan(ctx, rt, st, Extra{Remove: p.Remove})
	if err != nil {
		return err
	}
	logSkips(log, view)
	added, updated, removed, unchanged, recorded := Count(plan)
	log.Info("Plan: %d new, %d updated, %d removed, %d unchanged, %d recorded.", added, updated, removed, unchanged, recorded)
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		if err := store.SyncPlanned(ctx, tx, syncID, jsonOf(plan), db.At(st.ReadAt), store.Sync{
			Added: added, Updated: updated, Removed: removed, Unchanged: unchanged, Recorded: recorded,
		}); err != nil {
			return err
		}
		if err := store.RouterRead(ctx, tx, rt.ID, st.Untagged, len(st.Pins), db.At(st.ReadAt)); err != nil {
			return err
		}
		for _, tp := range plan {
			switch tp.Action {
			case Record:
				if err := setApplied(ctx, tx, rt.ID, tp.Tag, tp.Entries, now); err != nil {
					return err
				}
			case Forget:
				if err := store.DeleteRouterTag(ctx, tx, rt.ID, tp.Tag); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	header := fmt.Sprintf("proxier sync · router %s · job #%s", rt.Name, job)
	files := routeros.Files(names, job, header, Blocks(plan))
	if err := s.push(ctx, r, cl, rt, plan, files); err != nil {
		return s.fail(ctx, r, rt, syncID, p, err)
	}
	if len(files) > 0 {
		if err := s.verify(ctx, cl, names, plan); err != nil {
			return s.fail(ctx, r, rt, syncID, p, err)
		}
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		cur, err := store.GetRouter(ctx, tx, rt.ID)
		if err != nil {
			return err
		}
		if err := store.SyncDone(ctx, tx, syncID, "", now); err != nil {
			return err
		}
		if err := store.RouterSynced(ctx, tx, rt.ID, now); err != nil {
			return err
		}
		actor := r.Info().Actor()
		if added+updated+removed+recorded > 0 {
			if err := s.record(ctx, tx, "routing.router_synced", rt.ID, actor, map[string]any{
				"added": added, "updated": updated, "removed": removed, "recorded": recorded, "trigger": p.Trigger,
			}); err != nil {
				return err
			}
		}
		if cur.Failures > 0 {
			return s.record(ctx, tx, "routing.router_recovered", rt.ID, actor, map[string]any{
				"failures": cur.Failures, "notified": cur.FailureNotified,
			})
		}
		return nil
	})
}

// loadSync reads the payload and the router; ok is false when the router is
// gone or not active (the job ends quietly).
func (s *Service) loadSync(ctx context.Context, r *jobs.Run) (syncPayload, store.Router, bool, error) {
	var p syncPayload
	if err := r.Payload(&p); err != nil {
		return p, store.Router{}, false, jobs.Permanent(err)
	}
	rt, err := store.GetRouter(ctx, s.d.DB.R, p.RouterID)
	if errors.Is(err, store.ErrNotFound) {
		r.Log().Info("Router %d no longer exists: nothing to sync.", p.RouterID)
		return p, rt, false, nil
	}
	if err != nil {
		return p, rt, false, err
	}
	if rt.State != "active" {
		r.Log().Info("%s is %s: nothing to sync.", rt.Name, rt.State)
		return p, rt, false, nil
	}
	return p, rt, true, nil
}

// syncRow is the job's running row of a kind, created on the attempt's first
// step and reused by the next step and after a restart.
func (s *Service) syncRow(ctx context.Context, r *jobs.Run, rt store.Router, p syncPayload, kind string) (int64, error) {
	row, err := store.RunningSyncOfJob(ctx, s.d.DB.R, r.Info().ID, kind)
	if err == nil {
		return row.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	var id int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		id, err = store.InsertSync(ctx, tx, store.Sync{RouterID: rt.ID, JobID: r.Info().ID, Kind: kind, Trigger: p.Trigger, StartedAt: s.now()})
		return err
	})
	return id, err
}

func (s *Service) connect(ctx context.Context, r *jobs.Run, rt store.Router) (*sshx.Client, error) {
	return s.d.SSH.Connect(ctx, connOf(rt).Target(rt.ID), r.Log())
}

// read reads the router's DNS entries and address list.
func (s *Service) read(ctx context.Context, cl *sshx.Client, n routeros.Names) (State, error) {
	run := func(cmd string) ([]byte, error) {
		res, err := cl.Run(ctx, cmd, sshx.RunOptions{Timeout: readTimeout})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
		return res.Stdout, nil
	}
	at := s.d.Now()
	out, err := run(routeros.CmdReadDNS(n))
	if err != nil {
		return State{}, err
	}
	dns, err := routeros.ParseDNS(out)
	if err != nil {
		return State{}, err
	}
	if out, err = run(routeros.CmdReadList(n)); err != nil {
		return State{}, err
	}
	list, err := routeros.ParseList(out)
	if err != nil {
		return State{}, err
	}
	return Interpret(dns, list, n, at), nil
}

// plan computes the desired state (the router's infra pins left out) and
// the plan against what was read.
func (s *Service) plan(ctx context.Context, rt store.Router, st State, x Extra) ([]TagPlan, lists.View, error) {
	exclude := map[string]string{}
	for name, comment := range st.Pins {
		exclude[name] = fmt.Sprintf("it is an infra pin (%s) on %s", comment, rt.Name)
	}
	view, err := s.d.Lists.View(ctx, rt.ListID, exclude)
	if err != nil {
		return nil, view, err
	}
	applied, err := s.Applied(ctx, rt.ID)
	if err != nil {
		return nil, view, err
	}
	return MakePlan(Desired(view), st, applied, x), view, nil
}

func logSkips(log *jobs.Logger, v lists.View) {
	for _, o := range v.Result.Services {
		for _, d := range o.Dropped {
			if d.Reason == own.ReasonPinned {
				log.Info("%s: skipped, %s.", d.Name, d.Via)
			}
		}
	}
}

func setApplied(ctx context.Context, tx *sqlx.Tx, id int64, tag string, e []routeros.Entry, at db.Time) error {
	suffix, exact := routeros.Counts(e)
	return store.SetRouterTag(ctx, tx, id, store.RouterTag{Tag: tag, Hash: routeros.EntriesHash(e), Suffix: suffix, Exact: exact, AppliedAt: at})
}

// fail records a failed attempt before the step returns it: the router's
// failures, the sync row and routing.router_sync_failed, final (and
// notifying) on the job's last attempt or for an error no retry fixes. A
// shutdown or a cancel records nothing here.
func (s *Service) fail(ctx context.Context, r *jobs.Run, rt store.Router, syncID int64, p syncPayload, err error) error {
	select {
	case <-r.Cancelling():
		return jobs.ErrCancelled
	default:
	}
	if ctx.Err() != nil {
		return err
	}
	var f *failure
	if !errors.As(err, &f) {
		pr := ProblemOf(connOf(rt), err)
		f = &failure{step: StepConnect, hop: pr.Hop, text: pr.Text + Untouched, err: err}
		var unknown *sshx.UnknownHostError
		if errors.As(err, &unknown) {
			f.text = fmt.Sprintf("First contact with %s: confirm its fingerprint with Test connection.", unknown.Address) + Untouched
		}
	}
	info := r.Info()
	final := jobs.IsPermanent(err) || info.Attempt >= info.MaxAttempts
	r.Log().Error("%s failed: %s", f.step, f.text)
	werr := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		cur, err := store.GetRouter(ctx, tx, rt.ID)
		if err != nil {
			return err
		}
		if err := store.RouterFailed(ctx, tx, rt.ID, f.text, final, now); err != nil {
			return err
		}
		if err := store.SyncFailed(ctx, tx, syncID, f.step, f.hop, f.text, now); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.router_sync_failed", rt.ID, info.Actor(), map[string]any{
			"step": f.step, "error": f.text, "consecutive": cur.Failures + 1, "manual": p.Manual,
			"attempt": info.Attempt, "final": final,
		})
	})
	if werr != nil {
		return errors.Join(werr, err)
	}
	if jobs.IsPermanent(err) {
		return jobs.Permanent(f)
	}
	return f
}

// syncJobFailed records nothing (the attempt's own failure is the event):
// a row a step didn't end is marked failed.
func (s *Service) syncJobFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, err error) error {
	return store.SyncsOfJobFailed(ctx, tx, j.ID, err.Error(), s.now())
}

// syncJobCancelled marks the job's running row cancelled; nothing is counted.
func (s *Service) syncJobCancelled(ctx context.Context, tx *sqlx.Tx, j jobs.Info, _ string) error {
	return store.SyncsOfJobCancelled(ctx, tx, j.ID, s.now())
}

// keepQueued keeps a queued preview's payload: its row is the one shown.
func keepQueued(queued, _ json.RawMessage) (json.RawMessage, error) { return queued, nil }

// cancellable ends ctx when the admin cancels the job, so a long /import
// stops at once.
func cancellable(ctx context.Context, r *jobs.Run) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	go func() {
		select {
		case <-r.Cancelling():
			cancel()
		case <-stop:
		}
	}()
	return ctx, func() { close(stop); cancel() }
}

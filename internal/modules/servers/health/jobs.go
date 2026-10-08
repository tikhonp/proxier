package health

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// payload is what every per-server check job carries.
type payload struct {
	ServerID int64  `json:"server_id"`
	Kind     string `json:"kind,omitempty"` // external: scheduled or on_demand
	Skipped  string `json:"skipped,omitempty"`
}

func (s *Service) payloadOf(r *jobs.Run) (payload, error) {
	var p payload
	return p, r.Payload(&p)
}

// check is a one-shot step; cancel ends a waiting one at once.
func step(name string, fn func(ctx context.Context, r *jobs.Run) error) jobs.Step {
	return jobs.Step{Name: name, Run: func(ctx context.Context, r *jobs.Run) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-r.Cancelling():
				cancel()
			case <-ctx.Done():
			}
		}()
		err := fn(ctx, r)
		if err != nil {
			select {
			case <-r.Cancelling():
				return jobs.ErrCancelled
			default:
			}
		}
		return err
	}}
}

// JobTypes returns the check job types. They are quiet (a clean run is
// deleted after a day), run once (the next round is the retry) and need no
// failure event of their own: a failed one is a platform job.failed.
func (s *Service) JobTypes() []jobs.Type {
	typ := func(name string, q jobs.Queue, steps ...jobs.Step) jobs.Type {
		return jobs.Type{Name: name, Queue: q, MaxAttempts: 1, Quiet: true, Steps: steps}
	}
	return []jobs.Type{
		typ(JobReference, jobs.Checks, step("check", s.referenceStep)),
		typ(JobRound, jobs.Checks, step("enqueue", s.roundStep)),
		typ(JobSelfcheck, jobs.Checks, step("check", s.selfcheckStep), step("evaluate", s.evaluateStep)),
		typ(JobProxytest, jobs.Checks, step("test", s.proxytestStep), step("evaluate", s.evaluateStep)),
		typ(JobExternal, jobs.Checks, step("check", s.externalStep), step("evaluate", s.evaluateStep)),
		typ(JobExternalRound, jobs.Checks, step("enqueue", s.externalRoundStep)),
		typ(JobEvaluate, jobs.Checks, step("evaluate", s.evaluateStep)),
		typ(JobResume, jobs.Checks, step("resume", s.resumeStep)),
		typ(JobReminders, jobs.Checks, step("remind", s.remindersStep)),
		typ(JobStatsRollup, jobs.Maintenance, step("rollup", s.rollupStep)),
		typ(JobNodes, jobs.Maintenance, step("refresh", s.nodesStep)),
	}
}

// Schedules returns the rhythm of the checks (docs/build/1f.md, Decisions).
func (s *Service) Schedules() []jobs.Schedule {
	req := func(typ, key string) func(context.Context) (jobs.Request, error) {
		return func(context.Context) (jobs.Request, error) {
			return jobs.Request{Type: typ, CoalescingKey: key}, nil
		}
	}
	return []jobs.Schedule{
		{Name: "servers.reference", Every: time.Minute, Request: req(JobReference, "reference")},
		{Name: "servers.round", Every: 5 * time.Minute, Jitter: 30 * time.Second, Setting: conf.SelfcheckEvery, Request: req(JobRound, "round")},
		{Name: "servers.external_round", Every: 30 * time.Minute, Jitter: 3 * time.Minute, Setting: conf.ExternalEvery, Request: req(JobExternalRound, "external_round")},
		{Name: "servers.checkhost_nodes", At: "04:10", Request: req(JobNodes, "nodes")},
		{Name: "servers.reminders", Every: time.Hour, Request: req(JobReminders, "reminders")},
		{Name: "servers.stats_rollup", Every: time.Hour, Request: req(JobStatsRollup, "rollup")},
	}
}

// enqueueRound queues a server's self-check now and its proxy test ProxyDelay
// later, in tx. The self-check skips a server a mutating job is busy with; then
// the proxy test is not queued either (it would skip itself). It reports
// whether the server was skipped.
func (s *Service) enqueueRound(ctx context.Context, tx *sqlx.Tx, id int64, actor string, proxyDelay time.Duration) (skipped bool, err error) {
	sid := strconv.FormatInt(id, 10)
	_, err = s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobSelfcheck, ResourceKey: serverKey(id), SkipIfBusy: true, CoalescingKey: "selfcheck:" + sid,
		Payload: payload{ServerID: id}, CreatedBy: actor,
	})
	if errors.Is(err, jobs.ErrSkipped) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	// No resource key: a queued self-check on the same key would make SkipIfBusy
	// refuse it. The proxy test asks Busy when it starts instead.
	_, err = s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobProxytest, Subject: store.ServerSubject(id), CoalescingKey: "proxytest:" + sid,
		Delay: proxyDelay, Payload: payload{ServerID: id}, CreatedBy: actor,
	})
	return false, err
}

// roundStep queues the checks of every active, unpaused, not retiring server.
func (s *Service) roundStep(ctx context.Context, r *jobs.Run) error {
	ids, err := store.CheckableServerIDs(ctx, s.DB.R)
	if err != nil {
		return err
	}
	queued := 0
	for _, id := range ids {
		var skipped bool
		err := s.DB.Write(ctx, func(tx *sqlx.Tx) (err error) {
			skipped, err = s.enqueueRound(ctx, tx, id, r.Info().Actor(), s.ProxyDelay)
			return err
		})
		if err != nil {
			return err
		}
		if skipped {
			r.Log().Info("Server %d is busy with another job: skipped this round", id)
			continue
		}
		queued++
	}
	s.Jobs.Kick()
	r.Log().Info("Queued checks for %d of %d servers", queued, len(ids))
	return nil
}

// evaluateStep gives the verdict from the latest results. A skipped proxy
// test stored nothing, so there is nothing new to look at.
func (s *Service) evaluateStep(ctx context.Context, r *jobs.Run) error {
	p, err := s.payloadOf(r)
	if err != nil {
		return err
	}
	if p.Skipped != "" {
		r.Log().Info("Nothing to evaluate: %s", p.Skipped)
		return nil
	}
	return s.Evaluate(ctx, p.ServerID, r.Info().Actor())
}

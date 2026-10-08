package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

const (
	leaseFor      = 60 * time.Second
	leaseRenew    = 20 * time.Second
	reapEvery     = 10 * time.Second
	stallAfter    = 30 * time.Second
	maxErrorChars = 4000
)

// errLost means this runner no longer owns the job (another boot or the
// reaper took it); it stops without writing anything more.
var errLost = errors.New("jobs: job no longer owned by this runner")

// Start interrupts the orphans of other boots, then runs the pools, the
// scheduler and the reaper until ctx ends. It shuts down in order: stop
// claiming, give running steps Grace, mark what still runs interrupted.
func (s *System) Start(ctx context.Context) error {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	bg := context.WithoutCancel(ctx)
	if err := s.interruptOrphans(bg); err != nil {
		return fmt.Errorf("jobs: interrupt orphans: %w", err)
	}

	jobCtx, cancelJobs := context.WithCancel(bg)
	defer cancelJobs()
	var loops, work sync.WaitGroup
	for q, n := range Concurrency {
		loops.Add(1)
		go func() {
			defer loops.Done()
			s.poolLoop(ctx, jobCtx, &work, q, n)
		}()
	}
	loops.Add(2)
	go func() { defer loops.Done(); s.schedulerLoop(ctx) }()
	go func() { defer loops.Done(); s.reaperLoop(ctx) }()

	<-ctx.Done()
	loops.Wait() // nothing claims any more

	done := make(chan struct{})
	go func() { work.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.Grace):
	}
	cancelJobs()
	work.Wait()
	return s.interruptOwn(bg)
}

func (s *System) tick(name string) {
	s.mu.Lock()
	s.ticks[name] = s.Now()
	s.mu.Unlock()
}

func (s *System) poolLoop(ctx, jobCtx context.Context, work *sync.WaitGroup, q Queue, n int) {
	sem := make(chan struct{}, n)
	t := time.NewTicker(s.Poll)
	defer t.Stop()
	for {
		s.tick("queue:" + string(q))
		for len(sem) < n {
			id, ok, err := s.claim(ctx, q)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("jobs: claim", "queue", q, "error", err)
				}
				break
			}
			if !ok {
				break
			}
			sem <- struct{}{}
			work.Add(1)
			go func() {
				defer work.Done()
				defer s.Kick() // a job waiting for this resource key can start now
				defer func() { <-sem }()
				s.runJob(jobCtx, id)
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kickc[q]:
		}
	}
}

func kickChans() map[Queue]chan struct{} {
	m := make(map[Queue]chan struct{}, len(Concurrency))
	for q := range Concurrency {
		m[q] = make(chan struct{}, 1)
	}
	return m
}

// Kick wakes the pools after an enqueue, so a job doesn't wait for the poll.
func (s *System) Kick() {
	for _, c := range s.kickc {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// claim takes the oldest due job of the queue whose resource key has no
// running job, in one statement. An interrupted job is claimed without
// counting another attempt.
func (s *System) claim(ctx context.Context, q Queue) (id int64, ok bool, err error) {
	now := s.now()
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		e := tx.GetContext(ctx, &id, `
			UPDATE jobs SET state = 'running', attempt = attempt + (state = 'queued'), boot_id = ?, lease_until = ?,
				started_at = COALESCE(started_at, ?)
			WHERE id = (
				SELECT j.id FROM jobs j
				WHERE j.queue = ? AND j.state IN ('queued', 'interrupted') AND j.run_after <= ?
				  AND (j.resource_key = '' OR NOT EXISTS (
					SELECT 1 FROM jobs r WHERE r.resource_key = j.resource_key AND r.state = 'running'))
				ORDER BY j.run_after, j.id LIMIT 1)
			RETURNING id`, s.bootID, db.At(s.Now().Add(leaseFor)), now, string(q), now)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		ok = e == nil
		return e
	})
	return id, ok, err
}

// runJob runs the steps of a claimed job.
func (s *System) runJob(jobCtx context.Context, id int64) {
	bg := context.WithoutCancel(jobCtx)
	row, err := getRow(bg, s.d.R, id)
	if err != nil {
		s.log.Error("jobs: load claimed job", "id", id, "error", err)
		return
	}
	run := &Run{s: s, info: row.info(), payload: []byte(row.Payload), cancel: make(chan struct{})}
	run.log = newLogger(s, id)
	run.secrets, err = s.openSecrets(id, row.Secrets)
	if err != nil {
		s.log.Error("jobs: open secrets", "id", id, "error", err)
		run.secrets = map[string]string{}
	}
	for _, v := range run.secrets {
		run.log.Redact(v)
	}
	if !row.CancelRequestedAt.IsZero() {
		run.requestCancel(row.CancelRequestedBy.String)
	}
	s.mu.Lock()
	s.running[id] = run
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.running[id] == run {
			delete(s.running, id)
		}
		s.mu.Unlock()
	}()

	stopFlusher := s.flusher(bg, run)
	defer stopFlusher()

	t, ok := s.typeOf(row.Type)
	if !ok {
		s.finishFailed(bg, run, nil, "", Permanent(fmt.Errorf("%w: %q", ErrUnknownType, row.Type)))
		return
	}
	if err := s.ensureSteps(bg, id, t); err != nil {
		s.log.Error("jobs: steps", "id", id, "error", err)
		return
	}
	states, err := s.stepStates(bg, id)
	if err != nil {
		s.log.Error("jobs: steps", "id", id, "error", err)
		return
	}

	for _, st := range t.Steps {
		if states[st.Name] == "succeeded" {
			continue
		}
		if by, cancelled, err := s.cancelRequested(bg, id); err == nil && cancelled {
			s.finishCancelled(bg, run, &t, by)
			return
		}
		if err := s.startStep(bg, run, st.Name); err != nil {
			if !errors.Is(err, errLost) {
				s.log.Error("jobs: start step", "id", id, "error", err)
			}
			return
		}
		run.log.setStep(st.Name, run.info.Attempt)
		run.log.add("step", st.Name)
		err := s.safeStep(jobCtx, st, run)
		if jobCtx.Err() != nil && err != nil {
			// Shutdown cut the step: the job is marked interrupted, not failed.
			_ = run.log.Flush(bg)
			return
		}
		_ = run.log.Flush(bg)
		if err == nil {
			if err := s.endStep(bg, run, st.Name, "succeeded", ""); err != nil {
				if !errors.Is(err, errLost) {
					s.log.Error("jobs: end step", "id", id, "error", err)
				}
				return
			}
			continue
		}
		s.stepFailed(bg, run, &t, st.Name, err)
		return
	}
	s.finishSucceeded(bg, run)
}

func (s *System) safeStep(ctx context.Context, st Step, run *Run) (err error) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("jobs: step panicked", "job", run.info.ID, "step", st.Name, "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return st.Run(ctx, run)
}

func (s *System) ensureSteps(ctx context.Context, id int64, t Type) error {
	have, err := s.stepStates(ctx, id)
	if err != nil {
		return err
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		for _, st := range t.Steps {
			if _, ok := have[st.Name]; ok {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO job_steps (job_id, idx, name, state)
				SELECT ?, COALESCE(MAX(idx), -1) + 1, ?, 'pending' FROM job_steps WHERE job_id = ?`, id, st.Name, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *System) stepStates(ctx context.Context, id int64) (map[string]string, error) {
	var rows []struct {
		Name  string `db:"name"`
		State string `db:"state"`
	}
	if err := s.d.R.SelectContext(ctx, &rows, `SELECT name, state FROM job_steps WHERE job_id = ?`, id); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Name] = r.State
	}
	return m, nil
}

func (s *System) cancelRequested(ctx context.Context, id int64) (by string, yes bool, err error) {
	var r struct {
		At sql.NullString `db:"cancel_requested_at"`
		By sql.NullString `db:"cancel_requested_by"`
	}
	if err = s.d.R.GetContext(ctx, &r, `SELECT cancel_requested_at, cancel_requested_by FROM jobs WHERE id = ?`, id); err != nil {
		return
	}
	return r.By.String, r.At.Valid, nil
}

// owned runs fn in a write transaction if this runner still owns the job.
func (s *System) owned(ctx context.Context, run *Run, fn func(tx *sqlx.Tx) error) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		s.mu.Lock()
		cur := s.running[run.info.ID]
		s.mu.Unlock()
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM jobs WHERE id = ? AND state = 'running' AND boot_id = ?`,
			run.info.ID, s.bootID); err != nil {
			return err
		}
		if n == 0 || cur != run {
			return errLost
		}
		return fn(tx)
	})
}

func (s *System) startStep(ctx context.Context, run *Run, name string) error {
	return s.owned(ctx, run, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE job_steps SET state = 'running', attempt = ?, started_at = ?, finished_at = NULL, error = ''
			WHERE job_id = ? AND name = ?`, run.info.Attempt, s.now(), run.info.ID, name)
		return err
	})
}

func (s *System) endStep(ctx context.Context, run *Run, name, state, msg string) error {
	err := s.owned(ctx, run, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE job_steps SET state = ?, finished_at = ?, error = ? WHERE job_id = ? AND name = ?`,
			state, s.now(), cut(msg, maxErrorChars), run.info.ID, name)
		return err
	})
	s.hub.notify(run.info.ID)
	return err
}

// stepFailed decides what a failed step means: cancel, defer, retry or fail.
func (s *System) stepFailed(ctx context.Context, run *Run, t *Type, step string, err error) {
	var def deferral
	var perm permanent
	switch {
	case errors.Is(err, ErrCancelled):
		by, _, _ := s.cancelRequested(ctx, run.info.ID)
		_ = s.endStep(ctx, run, step, "cancelled", "")
		s.finishCancelled(ctx, run, t, by)
	case errors.As(err, &def):
		run.log.Warn("Deferred for %s: %s", def.d.Round(time.Millisecond), def.reason)
		_ = run.log.Flush(ctx)
		s.requeue(ctx, run, step, err, def.d, true)
	case errors.As(err, &perm) || run.info.Attempt >= run.info.MaxAttempts:
		s.finishFailed(ctx, run, t, step, err)
	default:
		wait := backoff(t, run.info.Attempt)
		run.log.Warn("Attempt %d of %d failed: %v. Next attempt in %s.", run.info.Attempt, run.info.MaxAttempts, err, wait)
		_ = run.log.Flush(ctx)
		s.requeue(ctx, run, step, err, wait, false)
	}
}

func backoff(t *Type, attempt int) time.Duration {
	if t == nil || len(t.Backoff) == 0 || attempt < 1 {
		return 0
	}
	return t.Backoff[min(attempt-1, len(t.Backoff)-1)]
}

func (s *System) requeue(ctx context.Context, run *Run, step string, cause error, wait time.Duration, uncounted bool) {
	err := s.owned(ctx, run, func(tx *sqlx.Tx) error {
		state := "failed"
		if uncounted {
			state = "pending"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE job_steps SET state = ?, finished_at = ?, error = ? WHERE job_id = ? AND name = ?`,
			state, s.now(), cut(cause.Error(), maxErrorChars), run.info.ID, step); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs SET state = 'queued', run_after = ?, lease_until = NULL, boot_id = '',
				attempt = attempt - ?, error = ?, error_step = ?
			WHERE id = ?`, db.At(s.Now().Add(wait)), uncounted, cut(cause.Error(), maxErrorChars), step, run.info.ID)
		return err
	})
	s.hub.notify(run.info.ID)
	if err != nil && !errors.Is(err, errLost) {
		s.log.Error("jobs: requeue", "id", run.info.ID, "error", err)
	}
}

func (s *System) finishSucceeded(ctx context.Context, run *Run) {
	_ = run.log.Flush(ctx)
	err := s.owned(ctx, run, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs SET state = 'succeeded', finished_at = ?, lease_until = NULL, secrets = NULL, error = '', error_step = ''
			WHERE id = ?`, s.now(), run.info.ID)
		return err
	})
	s.hub.notify(run.info.ID)
	if err != nil && !errors.Is(err, errLost) {
		s.log.Error("jobs: finish", "id", run.info.ID, "error", err)
	}
}

// finishFailed marks the job failed and records the failure event, in one
// transaction. t is nil when the type is unknown.
func (s *System) finishFailed(ctx context.Context, run *Run, t *Type, step string, cause error) {
	_ = run.log.Flush(ctx)
	run.log.Error("Failed: %v", cause)
	_ = run.log.Flush(ctx)
	err := s.owned(ctx, run, func(tx *sqlx.Tx) error {
		if step != "" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE job_steps SET state = 'failed', finished_at = ?, error = ? WHERE job_id = ? AND name = ?`,
				s.now(), cut(cause.Error(), maxErrorChars), run.info.ID, step); err != nil {
				return err
			}
		}
		return s.failTx(ctx, tx, run.info, t, step, cause)
	})
	s.hub.notify(run.info.ID)
	if err != nil && !errors.Is(err, errLost) {
		s.log.Error("jobs: fail", "id", run.info.ID, "error", err)
	}
}

// failTx marks job j failed and records its failure event inside tx.
func (s *System) failTx(ctx context.Context, tx *sqlx.Tx, j Info, t *Type, step string, cause error) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET state = 'failed', finished_at = ?, lease_until = NULL, error = ?, error_step = ? WHERE id = ?`,
		s.now(), cut(cause.Error(), maxErrorChars), step, j.ID); err != nil {
		return err
	}
	if t != nil && t.OnFailed != nil {
		if err := t.OnFailed(ctx, tx, j, cause); err == nil {
			return nil
		} else {
			s.log.Error("jobs: OnFailed", "id", j.ID, "type", j.Type, "error", err)
		}
	}
	_, err := s.ev.Record(ctx, tx, events.Event{
		Type: "job.failed", Actor: j.Actor(), Subject: events.Subject{Type: "job", ID: fmt.Sprint(j.ID)},
		Payload: map[string]any{"type": j.Type, "step": step, "error": cut(cause.Error(), 500)},
	})
	return err
}

func (s *System) finishCancelled(ctx context.Context, run *Run, t *Type, by string) {
	if by == "" {
		by = events.ActorAdmin
	}
	_ = run.log.Flush(ctx)
	err := s.owned(ctx, run, func(tx *sqlx.Tx) error {
		return s.cancelTx(ctx, tx, run.info, t, by, "Cancelled by "+by)
	})
	s.hub.notify(run.info.ID)
	if err != nil && !errors.Is(err, errLost) {
		s.log.Error("jobs: cancel", "id", run.info.ID, "error", err)
	}
}

func (s *System) cancelTx(ctx context.Context, tx *sqlx.Tx, j Info, t *Type, by, line string) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET state = 'cancelled', finished_at = ?, lease_until = NULL,
			cancel_requested_at = COALESCE(cancel_requested_at, ?), cancel_requested_by = COALESCE(cancel_requested_by, ?)
		WHERE id = ?`, s.now(), s.now(), by, j.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE job_steps SET state = 'cancelled' WHERE job_id = ? AND state IN ('pending', 'running')`, j.ID); err != nil {
		return err
	}
	if t != nil && t.OnCancelled != nil {
		if err := t.OnCancelled(ctx, tx, j, by); err != nil {
			return err
		}
	}
	return s.logSystem(ctx, tx, j.ID, "warn", "", j.Attempt, line)
}

// Cancel stops a job: at once when it is queued or interrupted, at the next
// step boundary (or inside a step that watches Cancelling) when it runs.
func (s *System) Cancel(ctx context.Context, id int64, by string) error {
	err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
		r, err := getRow(ctx, tx, id)
		if err != nil {
			return err
		}
		switch State(r.State) {
		case Queued, Interrupted:
			t, _ := s.typeOf(r.Type)
			tp := &t
			if t.Name == "" {
				tp = nil
			}
			return s.cancelTx(ctx, tx, r.info(), tp, by, "Cancelled by "+by)
		case Running:
			if _, err := tx.ExecContext(ctx, `
				UPDATE jobs SET cancel_requested_at = COALESCE(cancel_requested_at, ?),
					cancel_requested_by = COALESCE(cancel_requested_by, ?) WHERE id = ?`, s.now(), by, id); err != nil {
				return err
			}
			return s.logSystem(ctx, tx, id, "warn", "", r.Attempt, "Cancel requested by "+by)
		}
		return ErrNotActive
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	run := s.running[id]
	s.mu.Unlock()
	if run != nil {
		run.requestCancel(by)
	}
	s.hub.notify(id)
	return nil
}

// RetryOptions change what a retry carries over.
type RetryOptions struct {
	// Payload keys replace those of the failed job's payload (a shallow patch).
	Payload map[string]any
	// Secrets are added to the carried ones, replacing a secret of the same name.
	Secrets map[string]string
}

// Retry creates a new job linked to a failed or cancelled one. It carries the
// payload and secrets and starts at the step that did not finish.
func (s *System) Retry(ctx context.Context, id int64, by string) (newID int64, err error) {
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		newID, err = s.RetryWithTx(ctx, tx, id, by, RetryOptions{})
		return err
	})
	if err == nil {
		s.Kick()
	}
	return newID, err
}

// RetryWithTx is Retry inside the caller's write transaction, so the change
// that goes with a retry (a server back to provisioning) commits with it. The
// caller calls Kick after the commit. opts patch the payload and add secrets
// (the root password a provisioning retry asks for again).
func (s *System) RetryWithTx(ctx context.Context, tx *sqlx.Tx, id int64, by string, opts RetryOptions) (newID int64, err error) {
	r, err := getRow(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if st := State(r.State); st != Failed && st != Cancelled {
		return 0, ErrNotRetryable
	}
	t, ok := s.typeOf(r.Type)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownType, r.Type)
	}
	secrets, err := s.openSecrets(id, r.Secrets)
	if err != nil {
		return 0, err
	}
	for k, v := range opts.Secrets {
		secrets[k] = v
	}
	payload := json.RawMessage(r.Payload)
	if len(opts.Payload) > 0 {
		patch, err := marshalObject(opts.Payload)
		if err != nil {
			return 0, err
		}
		if payload, err = mergeObjects(payload, patch); err != nil {
			return 0, err
		}
		if len(payload) > MaxPayload {
			return 0, ErrPayloadTooLarge
		}
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO jobs (queue, type, resource_key, coalescing_key, subject_type, subject_id, state, payload,
			quiet, max_attempts, run_after, created_by, retry_of, created_at)
		SELECT queue, type, resource_key, coalescing_key, subject_type, subject_id, 'queued', ?,
			quiet, ?, ?, ?, id, ? FROM jobs WHERE id = ?`, string(payload), t.MaxAttempts, s.now(), by, s.now(), id)
	if err != nil {
		return 0, fmt.Errorf("jobs: retry: %w", err)
	}
	if newID, err = res.LastInsertId(); err != nil {
		return 0, err
	}
	if blob, err := s.sealSecrets(newID, secrets); err != nil {
		return 0, err
	} else if blob != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET secrets = ? WHERE id = ?`, blob, newID); err != nil {
			return 0, err
		}
	}
	var done []string
	if err := tx.SelectContext(ctx, &done, `SELECT name FROM job_steps WHERE job_id = ? AND state = 'succeeded'`, id); err != nil {
		return 0, err
	}
	carried := map[string]bool{}
	for _, n := range done {
		carried[n] = true
	}
	for i, st := range t.Steps {
		if carried[st.Name] {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO job_steps (job_id, idx, name, state, carried_from) VALUES (?, ?, ?, 'succeeded', ?)`,
				newID, i, st.Name, id)
		} else {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO job_steps (job_id, idx, name, state) VALUES (?, ?, ?, 'pending')`, newID, i, st.Name)
		}
		if err != nil {
			return 0, err
		}
	}
	return newID, s.logSystem(ctx, tx, newID, "info", "", 0, fmt.Sprintf("Retry of job #%d, started by %s", id, by))
}

// flusher writes the job's log every 250 ms or 100 lines and renews its lease.
// The returned function stops it after a last flush.
func (s *System) flusher(ctx context.Context, run *Run) (stop func()) {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(flushEvery * time.Millisecond)
		defer t.Stop()
		last := time.Now()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
			case <-run.log.kick:
			}
			if err := run.log.Flush(ctx); err != nil {
				s.log.Error("jobs: flush log", "id", run.info.ID, "error", err)
			}
			if time.Since(last) >= leaseRenew {
				last = time.Now()
				err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
					_, err := tx.ExecContext(ctx, `UPDATE jobs SET lease_until = ? WHERE id = ? AND state = 'running' AND boot_id = ?`,
						db.At(s.Now().Add(leaseFor)), run.info.ID, s.bootID)
					return err
				})
				if err != nil {
					s.log.Error("jobs: renew lease", "id", run.info.ID, "error", err)
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-done
		_ = run.log.Flush(ctx)
	}
}

// interruptOrphans marks running jobs of other boots interrupted: one process
// owns the database, so they cannot still be running.
func (s *System) interruptOrphans(ctx context.Context) error {
	var ids []int64
	if err := s.d.R.SelectContext(ctx, &ids, `SELECT id FROM jobs WHERE state = 'running' AND boot_id != ?`, s.bootID); err != nil {
		return err
	}
	return s.interrupt(ctx, ids, "Interrupted by restart")
}

// interruptOwn marks this boot's running jobs interrupted at shutdown.
func (s *System) interruptOwn(ctx context.Context) error {
	var ids []int64
	if err := s.d.R.SelectContext(ctx, &ids, `SELECT id FROM jobs WHERE state = 'running' AND boot_id = ?`, s.bootID); err != nil {
		return err
	}
	return s.interrupt(ctx, ids, "Interrupted by restart")
}

func (s *System) interrupt(ctx context.Context, ids []int64, line string) error {
	var errs []error
	for _, id := range ids {
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
		errs = append(errs, s.d.Write(ctx, func(tx *sqlx.Tx) error {
			r, err := getRow(ctx, tx, id)
			if err != nil || State(r.State) != Running {
				return err
			}
			if t, ok := s.typeOf(r.Type); ok && t.NoResume {
				if err := s.logSystem(ctx, tx, id, "error", "", r.Attempt, line+": this job cannot resume"); err != nil {
					return err
				}
				return s.failTx(ctx, tx, r.info(), &t, "", errors.New("interrupted by restart"))
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE jobs SET state = 'interrupted', run_after = ?, lease_until = NULL WHERE id = ?`, s.now(), id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE job_steps SET state = 'pending' WHERE job_id = ? AND state = 'running'`, id); err != nil {
				return err
			}
			return s.logSystem(ctx, tx, id, "warn", "", r.Attempt, line)
		}))
		s.hub.notify(id)
	}
	return errors.Join(errs...)
}

func (s *System) reaperLoop(ctx context.Context) {
	t := time.NewTicker(reapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reap(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("jobs: reap", "error", err)
			}
		}
	}
}

// Reap interrupts running jobs of this boot whose lease expired: their runner
// is stuck.
func (s *System) Reap(ctx context.Context) error {
	var ids []int64
	if err := s.d.R.SelectContext(ctx, &ids, `
		SELECT id FROM jobs WHERE state = 'running' AND boot_id = ? AND lease_until < ?`, s.bootID, s.now()); err != nil {
		return err
	}
	return s.interrupt(ctx, ids, "Interrupted: the runner stopped responding")
}

// Health is nil while every pool and the scheduler keep looping and no lease
// has expired.
func (s *System) Health() error {
	s.mu.Lock()
	started := s.started
	var stalled []string
	for name, at := range s.ticks {
		if s.Now().Sub(at) > stallAfter {
			stalled = append(stalled, name)
		}
	}
	s.mu.Unlock()
	if !started {
		return nil
	}
	if len(stalled) > 0 {
		return fmt.Errorf("jobs: stalled: %v", stalled)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var n int
	if err := s.d.R.GetContext(ctx, &n, `
		SELECT count(*) FROM jobs WHERE state = 'running' AND boot_id = ? AND lease_until < ?`, s.bootID, s.now()); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("jobs: %d running job(s) with an expired lease", n)
	}
	return nil
}

package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Enqueue inserts a job inside tx, the caller's write transaction, so a change
// and the job it causes commit together. A request whose coalescing key
// matches a queued job that has never started merges into it instead.
func (s *System) Enqueue(ctx context.Context, tx *sqlx.Tx, r Request) (Enqueued, error) {
	t, ok := s.typeOf(r.Type)
	if !ok {
		return Enqueued{}, fmt.Errorf("%w: %q", ErrUnknownType, r.Type)
	}
	if r.CreatedBy == "" {
		return Enqueued{}, errors.New("jobs: request without CreatedBy")
	}
	payload, err := marshalObject(r.Payload)
	if err != nil {
		return Enqueued{}, err
	}
	if len(payload) > MaxPayload {
		return Enqueued{}, ErrPayloadTooLarge
	}

	if r.SkipIfBusy && r.ResourceKey != "" {
		var n int
		if err := tx.GetContext(ctx, &n, `
			SELECT count(*) FROM jobs WHERE resource_key = ? AND state IN ('running', 'queued', 'interrupted')`,
			r.ResourceKey); err != nil {
			return Enqueued{}, err
		}
		if n > 0 {
			return Enqueued{}, ErrSkipped
		}
	}

	runAfter := db.At(s.Now().Add(r.Delay))
	if r.CoalescingKey != "" {
		var q struct {
			ID       int64   `db:"id"`
			Payload  string  `db:"payload"`
			Secrets  []byte  `db:"secrets"`
			RunAfter db.Time `db:"run_after"`
		}
		err := tx.GetContext(ctx, &q, `
			SELECT id, payload, secrets, run_after FROM jobs
			WHERE type = ? AND coalescing_key = ? AND state = 'queued' AND attempt = 0`, r.Type, r.CoalescingKey)
		if err == nil {
			return s.merge(ctx, tx, t, r, payload, runAfter, q.ID, q.Payload, q.Secrets, q.RunAfter)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Enqueued{}, err
		}
	}

	subject := r.Subject
	if subject.Type == "" {
		subject = parseResourceKey(r.ResourceKey)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO jobs (queue, type, resource_key, coalescing_key, subject_type, subject_id, state, payload,
			quiet, max_attempts, run_after, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?)`,
		t.Queue, t.Name, r.ResourceKey, r.CoalescingKey, subject.Type, subject.ID, string(payload),
		t.Quiet, t.MaxAttempts, runAfter, r.CreatedBy, s.now())
	if err != nil {
		return Enqueued{}, fmt.Errorf("jobs: enqueue %s: %w", r.Type, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Enqueued{}, err
	}
	if len(r.Secrets) > 0 {
		blob, err := s.sealSecrets(id, r.Secrets)
		if err != nil {
			return Enqueued{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET secrets = ? WHERE id = ?`, blob, id); err != nil {
			return Enqueued{}, err
		}
	}
	for i, st := range t.Steps {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO job_steps (job_id, idx, name, state) VALUES (?, ?, ?, 'pending')`, id, i, st.Name); err != nil {
			return Enqueued{}, err
		}
	}
	return Enqueued{ID: id}, nil
}

func (s *System) merge(ctx context.Context, tx *sqlx.Tx, t Type, r Request, incoming json.RawMessage,
	runAfter db.Time, id int64, queued string, secrets []byte, queuedRunAfter db.Time) (Enqueued, error) {
	var merged json.RawMessage
	var err error
	if t.Merge != nil {
		merged, err = t.Merge(json.RawMessage(queued), incoming)
	} else {
		merged, err = mergeObjects(json.RawMessage(queued), incoming)
	}
	if err != nil {
		return Enqueued{}, fmt.Errorf("jobs: merge into job %d: %w", id, err)
	}
	if len(merged) > MaxPayload {
		return Enqueued{}, ErrPayloadTooLarge
	}
	if queuedRunAfter.After(runAfter.Time) {
		runAfter = queuedRunAfter
	}
	blob := secrets
	if len(r.Secrets) > 0 {
		cur, err := s.openSecrets(id, secrets)
		if err != nil {
			return Enqueued{}, err
		}
		maps.Copy(cur, r.Secrets)
		if blob, err = s.sealSecrets(id, cur); err != nil {
			return Enqueued{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET payload = ?, secrets = ?, run_after = ?, merged = merged + 1 WHERE id = ?`,
		string(merged), blob, runAfter, id); err != nil {
		return Enqueued{}, err
	}
	return Enqueued{ID: id, Merged: true}, nil
}

// EnqueueNow enqueues in its own transaction.
func (s *System) EnqueueNow(ctx context.Context, r Request) (e Enqueued, err error) {
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		e, err = s.Enqueue(ctx, tx, r)
		return err
	})
	if err == nil {
		s.Kick()
	}
	return e, err
}

func marshalObject(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jobs: payload: %w", err)
	}
	if b = bytes.TrimSpace(b); len(b) == 0 || b[0] != '{' {
		if string(b) == "null" {
			return json.RawMessage("{}"), nil
		}
		return nil, errors.New("jobs: payload must be a JSON object")
	}
	return b, nil
}

func mergeObjects(a, b json.RawMessage) (json.RawMessage, error) {
	var ma, mb map[string]json.RawMessage
	if err := json.Unmarshal(a, &ma); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &mb); err != nil {
		return nil, err
	}
	if ma == nil {
		ma = map[string]json.RawMessage{}
	}
	maps.Copy(ma, mb)
	return json.Marshal(ma)
}

// RunNow moves a queued job's run-after to now ("Retry now" on a job waiting
// for its backoff).
func (s *System) RunNow(ctx context.Context, id int64) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE jobs SET run_after = ? WHERE id = ? AND state = 'queued'`, s.now(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotActive
		}
		return nil
	})
}

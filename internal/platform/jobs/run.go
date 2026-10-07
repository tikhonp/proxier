package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jmoiron/sqlx"
)

// Run is what a step gets: the job's identity, payload, secrets and log.
type Run struct {
	s    *System
	info Info
	log  *Logger

	mu       sync.Mutex
	payload  json.RawMessage
	secrets  map[string]string
	cancel   chan struct{}
	cancelBy string
	canceled bool
}

// Info describes the job.
func (r *Run) Info() Info { return r.info }

// Log is the job's logger.
func (r *Run) Log() *Logger { return r.log }

// Payload decodes the job's payload into v.
func (r *Run) Payload(v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return json.Unmarshal(r.payload, v)
}

// SavePayload replaces the payload with v, for results later steps need. It
// runs in its own transaction.
func (r *Run) SavePayload(ctx context.Context, v any) error {
	b, err := marshalObject(v)
	if err != nil {
		return err
	}
	if len(b) > MaxPayload {
		return ErrPayloadTooLarge
	}
	err = r.s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET payload = ? WHERE id = ?`, string(b), r.info.ID)
		return err
	})
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.payload = b
	r.mu.Unlock()
	return nil
}

// Secret returns a secret of the request by name.
func (r *Run) Secret(name string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.secrets[name]
	return v, ok, nil
}

// DeleteSecret removes a secret from the stored job (the root password after
// key login works). It stays registered for redaction.
func (r *Run) DeleteSecret(ctx context.Context, name string) error {
	r.mu.Lock()
	if _, ok := r.secrets[name]; !ok {
		r.mu.Unlock()
		return nil
	}
	rest := map[string]string{}
	for k, v := range r.secrets {
		if k != name {
			rest[k] = v
		}
	}
	r.mu.Unlock()
	blob, err := r.s.sealSecrets(r.info.ID, rest)
	if err != nil {
		return err
	}
	err = r.s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET secrets = ? WHERE id = ?`, blob, r.info.ID)
		return err
	})
	if err != nil {
		return fmt.Errorf("jobs: delete secret: %w", err)
	}
	r.mu.Lock()
	r.secrets = rest
	r.mu.Unlock()
	return nil
}

// Cancelling is closed when the admin asks to cancel; a step that can stop
// mid-way returns ErrCancelled.
func (r *Run) Cancelling() <-chan struct{} { return r.cancel }

func (r *Run) requestCancel(by string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.canceled {
		r.canceled, r.cancelBy = true, by
		close(r.cancel)
	}
}

// Package change is the seam through which every change that can alter what
// a target gets marks its targets for sync (build README, Phase 3: "The sync
// seam"). Until router sync exists (3e) nothing listens.
package change

import (
	"context"
	"sync"

	"github.com/jmoiron/sqlx"
)

// Change names what may now differ for targets. Every field is optional.
type Change struct {
	Lists    []int64 // lists whose services or order changed
	Services []int64 // services whose accepted snapshot or tag changed: every list holding them
	Routers  []int64 // routers whose own list changed (3e)
	Actor    string
	Why      string // for the sync record: "anthropic changed", "Main reordered"
}

// Marker is called inside the transaction of the change it marks.
type Marker interface {
	Mark(ctx context.Context, tx *sqlx.Tx, c Change) error
}

// None marks nothing.
type None struct{}

func (None) Mark(context.Context, *sqlx.Tx, Change) error { return nil }

// Recorder keeps every change; tests use it until 3e.
type Recorder struct {
	mu      sync.Mutex
	changes []Change
}

func (r *Recorder) Mark(_ context.Context, _ *sqlx.Tx, c Change) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, c)
	return nil
}

// Changes returns a copy of what was marked, oldest first.
func (r *Recorder) Changes() []Change {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Change(nil), r.changes...)
}

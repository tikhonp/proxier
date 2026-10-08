package substest

import (
	"context"
	"strconv"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// FakeRotate is the job type the fake rotator requests. The harness
// registers it; its step does nothing, and no worker runs it unless a test
// starts the jobs.
const FakeRotate = "fake.rotate"

// Rotator is a fake servers.Rotator: it answers requests of FakeRotate (payload
// server_id, resource key server:<id>), or the error a test set for a server.
type Rotator struct {
	mu      sync.Mutex
	refused map[int64]error
}

// Refuse makes requests for the server fail with err (servers.ErrNotActive,
// servers.ErrNothingToRotate); nil accepts it again.
func (r *Rotator) Refuse(serverID int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refused == nil {
		r.refused = map[int64]error{}
	}
	r.refused[serverID] = err
}

func (r *Rotator) RotationRequest(_ context.Context, serverID int64, actor string) (jobs.Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refused[serverID]; err != nil {
		return jobs.Request{}, err
	}
	return jobs.Request{
		Type: FakeRotate, Payload: map[string]any{"server_id": serverID},
		ResourceKey: "server:" + strconv.FormatInt(serverID, 10), CreatedBy: actor,
	}, nil
}

var _ servers.Rotator = (*Rotator)(nil)

func fakeRotateType() jobs.Type {
	return jobs.Type{Name: FakeRotate, Queue: jobs.Maintenance, MaxAttempts: 1, Steps: []jobs.Step{
		{Name: "rotate", Run: func(context.Context, *jobs.Run) error { return nil }},
	}}
}

// Rotated feeds the cut-off subscriber, inside a Write, the event a real
// rotation records when it succeeds: actor job:<jobID>, subject server:<id>.
func (h *Harness) Rotated(jobID, serverID int64) {
	h.T.Helper()
	h.feed(events.Event{Type: "server.credentials_rotated", Payload: map[string]any{"keys": []any{"client_uuid"}}}, jobID, serverID)
}

// RotationFailed feeds the event a failed rotation records (kind rotate).
func (h *Harness) RotationFailed(jobID, serverID int64, msg string) {
	h.T.Helper()
	h.feed(events.Event{Type: "server.redeploy_failed", Payload: map[string]any{"kind": "rotate", "step": "proxy-test", "error": msg, "restored": true}}, jobID, serverID)
}

// Feed hands the cut-off subscriber any event, as the dispatcher would.
func (h *Harness) Feed(e events.Event) {
	h.T.Helper()
	h.mu.Lock()
	h.fed++
	e.ID = 100000 + h.fed
	h.mu.Unlock()
	e.Module = "servers"
	sub := h.Mod.Links.CutOffSubscriber()
	if err := h.App.DB.Write(context.Background(), func(tx *sqlx.Tx) error { return sub.Handle(context.Background(), tx, e) }); err != nil {
		h.T.Fatalf("cut-off subscriber on %s: %v", e.Type, err)
	}
}

func (h *Harness) feed(e events.Event, jobID, serverID int64) {
	h.T.Helper()
	e.Actor = "job:" + strconv.FormatInt(jobID, 10)
	e.Subject = events.Subject{Type: "server", ID: strconv.FormatInt(serverID, 10)}
	h.Feed(e)
}

package links

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Cut off disables a link and rotates every server of its subscription, one at
// a time (docs/processes/servers/credential-rotation.md#cut-off): an event
// subscriber starts the next rotation when the previous one ends.

var (
	// ErrNoRotator: the servers module offers no rotation, so there is no cut-off.
	ErrNoRotator = errors.New("links: no rotator")
	// ErrNotRetryable: the server's rotation hasn't failed.
	ErrNotRetryable = errors.New("links: only a failed rotation can be retried")
)

// Why a server is skipped. Pages translate them (cutoff.reason.*).
const (
	ReasonNotInService = "not in service"
	ReasonNothing      = "no rotatable values"
)

// CutOffSubscriberName is the subscriber that moves cut-offs on.
const CutOffSubscriberName = "subscriptions.cutoff"

// CutOffPlan is what Cut off will do, shown before it starts.
type CutOffPlan struct {
	Link   Link
	Rotate []PlanServer // in subscription order
	Skip   []PlanServer // with Reason
	// OtherLinks are the active, unexpired links other than this one whose
	// subscription holds a server that will rotate; OtherSubs their subscriptions.
	OtherLinks int
	OtherSubs  int
}

// PlanServer is a member of the link's subscription.
type PlanServer struct {
	ServerID     int64
	Name, Health string // Health "" when not in service
	Reason       string // skipped only
}

// CutOff is a cut-off with its servers.
type CutOff struct {
	ID        int64
	CreatedAt time.Time
	CreatedBy string
	Items     []CutOffItem
}

// Busy: a rotation waits or runs.
func (c CutOff) Busy() bool {
	for _, it := range c.Items {
		if it.State == "waiting" || it.State == "running" {
			return true
		}
	}
	return false
}

// CutOffItem is one server of a cut-off.
type CutOffItem struct {
	Position   int
	ServerID   int64
	ServerName string
	State      string // waiting, running, done, failed, skipped
	JobID      int64
	Error      string // failed: the rotation's error; skipped: the reason
}

// CanCutOff: the servers module offers rotation.
func (s *Service) CanCutOff() bool { return s.d.Rotator != nil }

// request asks the rotator for the server's rotation; reason is why it can't
// be rotated instead.
func (s *Service) request(ctx context.Context, serverID int64, actor string) (req jobs.Request, reason string, err error) {
	req, err = s.d.Rotator.RotationRequest(ctx, serverID, actor)
	switch {
	case errors.Is(err, servers.ErrNotActive):
		return req, ReasonNotInService, nil
	case errors.Is(err, servers.ErrNothingToRotate):
		return req, ReasonNothing, nil
	}
	return req, "", err
}

// PlanCutOff says what Cut off would do; nothing is enqueued.
func (s *Service) PlanCutOff(ctx context.Context, linkID int64) (CutOffPlan, error) {
	if s.d.Rotator == nil {
		return CutOffPlan{}, ErrNoRotator
	}
	l, err := s.Get(ctx, linkID)
	if err != nil {
		return CutOffPlan{}, err
	}
	if l.State == "deleted" {
		return CutOffPlan{}, ErrDeleted
	}
	members, err := s.d.Subs.Members(ctx, l.SubscriptionID)
	if err != nil {
		return CutOffPlan{}, err
	}
	p := CutOffPlan{Link: l}
	var rotating []int64
	for _, m := range members {
		ps := PlanServer{ServerID: m.ServerID, Name: m.Name}
		if m.InService {
			ps.Health = m.Server.Health
		}
		if _, ps.Reason, err = s.request(ctx, m.ServerID, "admin"); err != nil {
			return CutOffPlan{}, err
		}
		if ps.Reason != "" {
			p.Skip = append(p.Skip, ps)
			continue
		}
		p.Rotate = append(p.Rotate, ps)
		rotating = append(rotating, m.ServerID)
	}
	p.OtherLinks, p.OtherSubs, err = store.OtherActiveLinks(ctx, s.d.DB.R, rotating, linkID, s.now())
	return p, err
}

// CutOff disables the link (when active) and queues the rotation of the first
// server of its subscription; the others follow one at a time.
func (s *Service) CutOff(ctx context.Context, linkID int64, actor string) (int64, error) {
	if s.d.Rotator == nil {
		return 0, ErrNoRotator
	}
	var id int64
	err := s.change(ctx, linkID, func(tx *sqlx.Tx, l store.Link) error {
		if l.State == "active" {
			if err := store.SetLinkState(ctx, tx, linkID, "disabled", s.now(), db.Time{}); err != nil {
				return err
			}
			if err := s.record(ctx, tx, "link.disabled", linkID, actor, nil); err != nil {
				return err
			}
		}
		members, err := store.Members(ctx, tx, l.SubscriptionID.Int64)
		if err != nil {
			return err
		}
		if id, err = store.InsertCutOff(ctx, tx, linkID, s.now(), actor); err != nil {
			return err
		}
		// A server that can't be rotated is skipped now, so the page names it
		// at once and the event lists it.
		var rotate, skipped []string
		for i, m := range members {
			it := store.CutOffItem{CutOffID: id, Position: i + 1, ServerID: m.ServerID, ServerName: m.ServerName, State: "waiting"}
			if _, it.Error, err = s.request(ctx, m.ServerID, actor); err != nil {
				return err
			}
			if it.Error != "" {
				it.State = "skipped"
				skipped = append(skipped, m.ServerName)
			} else {
				rotate = append(rotate, m.ServerName)
			}
			if err := store.InsertCutOffItem(ctx, tx, it); err != nil {
				return err
			}
		}
		if err := s.advance(ctx, tx, id, actor); err != nil {
			return err
		}
		return s.record(ctx, tx, "link.cut_off", linkID, actor, map[string]any{
			"servers": strings.Join(rotate, ", "), "skipped": strings.Join(skipped, ", "),
		})
	})
	if err != nil {
		return 0, err
	}
	s.d.Jobs.Kick()
	return id, nil
}

// advance queues the next waiting server's rotation when none runs. A server
// that can't be rotated any more is skipped and the next one tried.
func (s *Service) advance(ctx context.Context, tx *sqlx.Tx, cutoffID int64, actor string) error {
	items, err := store.CutOffItems(ctx, tx, cutoffID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.State == "running" {
			return nil
		}
	}
	for _, it := range items {
		if it.State != "waiting" {
			continue
		}
		req, reason, err := s.request(ctx, it.ServerID, actor)
		if err != nil {
			return err
		}
		if reason != "" {
			if err := store.SetCutOffItem(ctx, tx, cutoffID, it.Position, "skipped", 0, reason); err != nil {
				return err
			}
			continue
		}
		e, err := s.d.Jobs.Enqueue(ctx, tx, req)
		if err != nil {
			return err
		}
		return store.SetCutOffItem(ctx, tx, cutoffID, it.Position, "running", e.ID, "")
	}
	return nil
}

// RetryCutOff queues a failed server of the link's latest cut-off again: at
// once when nothing runs, else after the running one.
func (s *Service) RetryCutOff(ctx context.Context, linkID int64, position int, actor string) error {
	if s.d.Rotator == nil {
		return ErrNoRotator
	}
	err := s.change(ctx, linkID, func(tx *sqlx.Tx, _ store.Link) error {
		c, err := store.LatestCutOff(ctx, tx, linkID)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotRetryable
		}
		if err != nil {
			return err
		}
		items, err := store.CutOffItems(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		for _, it := range items {
			if it.Position != position {
				continue
			}
			if it.State != "failed" {
				return ErrNotRetryable
			}
			if err := store.SetCutOffItem(ctx, tx, c.ID, position, "waiting", 0, ""); err != nil {
				return err
			}
			return s.advance(ctx, tx, c.ID, actor)
		}
		return ErrNotRetryable
	})
	if err == nil {
		s.d.Jobs.Kick()
	}
	return err
}

// LatestCutOff is the newest cut-off of the link; false when it has none.
func (s *Service) LatestCutOff(ctx context.Context, linkID int64) (CutOff, bool, error) {
	c, err := store.LatestCutOff(ctx, s.d.DB.R, linkID)
	if errors.Is(err, store.ErrNotFound) {
		return CutOff{}, false, nil
	}
	if err != nil {
		return CutOff{}, false, err
	}
	rows, err := store.CutOffItems(ctx, s.d.DB.R, c.ID)
	if err != nil {
		return CutOff{}, false, err
	}
	out := CutOff{ID: c.ID, CreatedAt: c.CreatedAt.Time, CreatedBy: c.CreatedBy, Items: make([]CutOffItem, len(rows))}
	for i, r := range rows {
		out.Items[i] = CutOffItem{
			Position: r.Position, ServerID: r.ServerID, ServerName: r.ServerName, State: r.State, JobID: r.JobID.Int64, Error: r.Error,
		}
	}
	return out, true, nil
}

// CutOffSubscriber is subscriptions.cutoff: when a cut-off's rotation ends,
// it records how and starts the next one. A failure doesn't stop the rest.
func (s *Service) CutOffSubscriber() events.Subscriber {
	return events.Subscriber{
		Name:   CutOffSubscriberName,
		Types:  []string{"server.credentials_rotated", "server.redeploy_failed"},
		Handle: s.onRotationEnded,
	}
}

func (s *Service) onRotationEnded(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
	if s.d.Rotator == nil {
		return nil
	}
	jobID, ok := jobOf(e.Actor)
	if !ok {
		return nil
	}
	if e.Type == "server.redeploy_failed" {
		if kind, _ := e.Payload["kind"].(string); kind != "rotate" {
			return nil
		}
	}
	it, err := store.RunningCutOffItem(ctx, tx, jobID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // already handled, or not a cut-off's rotation
	}
	if err != nil {
		return err
	}
	state, msg := "done", ""
	if e.Type == "server.redeploy_failed" {
		state = "failed"
		msg, _ = e.Payload["error"].(string)
		if cancelled, _ := e.Payload["cancelled"].(bool); cancelled {
			msg = "cancelled"
		}
	}
	if err := store.SetCutOffItem(ctx, tx, it.CutOffID, it.Position, state, jobID, msg); err != nil {
		return err
	}
	// The workers find the queued job on their next poll: a kick here would
	// come before the dispatcher commits.
	return s.advance(ctx, tx, it.CutOffID, "event:"+strconv.FormatInt(e.ID, 10))
}

// jobOf is the job id of a "job:<id>" actor.
func jobOf(actor string) (int64, bool) {
	rest, ok := strings.CutPrefix(actor, "job:")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil
}

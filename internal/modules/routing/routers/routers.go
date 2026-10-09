// Package routers keeps MikroTik routers equal to their routing list (3e):
// adding and testing them, the pure plan, sync and preview jobs, the marker
// that turns changes into coalesced syncs, and their history. Drift, unmanaged
// tags, awaiting setup, pausing and removal are 3f's.
package routers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
)

var (
	ErrNotFound  = errors.New("routers: no such router")
	ErrNotActive = errors.New("routers: the router is not active")
	// ErrTestNotFound is returned for an unknown test; ErrNotConfirming for
	// a test that isn't waiting at a fingerprint.
	ErrTestNotFound  = errors.New("routers: no such test")
	ErrNotConfirming = errors.New("routers: the test isn't waiting for a confirmation")
)

// Job types.
const (
	JobSync    = "routing.sync"
	JobPreview = "routing.preview"
	JobTest    = "routing.test"
)

// MaxName is the longest router name.
const MaxName = 60

// Router is a router as the pages and jobs see it.
type Router struct {
	ID              int64
	Name            string
	ListID          int64
	List            string
	State           string // awaiting, active, paused, removing
	Conn            Connection
	Version         string
	Board           string
	ConnectedAt     time.Time
	LastSeenAt      time.Time
	LastSyncAt      time.Time
	LastResult      string // "", synced, failed
	LastError       string
	Failures        int
	FailureNotified bool
	Untagged        int
	InfraPins       int
	ReadAt          time.Time
	CreatedBy       string
	CreatedAt       time.Time
}

// Connected reports whether the router has ever connected: until then
// changes queue nothing for it.
func (r Router) Connected() bool { return !r.ConnectedAt.IsZero() }

func routerOf(r store.Router) Router {
	return Router{
		ID: r.ID, Name: r.Name, ListID: r.ListID, List: r.List, State: r.State, Conn: connOf(r), Version: r.Version, Board: r.Board,
		ConnectedAt: r.ConnectedAt.Time, LastSeenAt: r.LastSeenAt.Time, LastSyncAt: r.LastSyncAt.Time, LastResult: r.LastSyncResult,
		LastError: r.LastSyncError, Failures: r.Failures, FailureNotified: r.FailureNotified, Untagged: r.Untagged, InfraPins: r.InfraPins,
		ReadAt: r.ReadAt.Time, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.Time,
	}
}

// Subject is a router's event subject.
func Subject(id int64) events.Subject { return events.Subject{Type: "router", ID: fmt.Sprint(id)} }

func resourceKey(id int64) string { return fmt.Sprintf("router:%d", id) }

// Deps are what the service uses.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Settings *settings.Store
	Jobs     *jobs.System
	SSH      *sshx.SSH
	Tailnet  *tailnet.Node // may be nil
	Lists    *lists.Service
	Services *services.Service
	Now      func() time.Time
	Log      *slog.Logger
}

// Service is router sync.
type Service struct {
	d Deps
}

// New builds the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

func (s *Service) now() db.Time { return db.At(s.d.Now()) }

// TailnetRunning reports whether the first hop can go through the tailnet:
// the add form's default.
func (s *Service) TailnetRunning(ctx context.Context) bool {
	return s.d.Tailnet != nil && s.d.Tailnet.Status(ctx).State == tailnet.Running
}

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, id int64, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Type: typ, Subject: Subject(id), Actor: actor, Payload: payload, Time: s.now()})
	return err
}

// Get reads a router.
func (s *Service) Get(ctx context.Context, id int64) (Router, error) {
	r, err := store.GetRouter(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Router{}, ErrNotFound
	}
	return routerOf(r), err
}

// List reads every router by name.
func (s *Service) List(ctx context.Context) ([]Router, error) {
	rows, err := store.Routers(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	out := make([]Router, 0, len(rows))
	for _, r := range rows {
		out = append(out, routerOf(r))
	}
	return out, nil
}

// ByList reads the routers following a list, by name.
func (s *Service) ByList(ctx context.Context, listID int64) ([]Router, error) {
	rows, err := store.RoutersOfList(ctx, s.d.DB.R, listID)
	if err != nil {
		return nil, err
	}
	out := make([]Router, 0, len(rows))
	for _, r := range rows {
		out = append(out, routerOf(r))
	}
	return out, nil
}

// checkName validates a router's name inside q; the values are i18n keys.
func checkName(ctx context.Context, q sqlx.QueryerContext, name string, except int64, fe store.FieldErrors) error {
	switch {
	case name == "" || utf8.RuneCountInString(name) > MaxName:
		fe["name"] = "routers.err.name_length"
	default:
		taken, err := store.RouterNameTaken(ctx, q, name, except)
		if err != nil {
			return err
		}
		if taken {
			fe["name"] = "routers.err.name_taken"
		}
	}
	return nil
}

// Create saves a router. With testID the add form's latest test: when it
// passed (or warned) with exactly these values, the router is connected and
// its initial sync is queued; otherwise (testID 0 or other values) it is
// saved untested and waits for a passing test.
func (s *Service) Create(ctx context.Context, name string, listID int64, c Connection, testID int64, actor string) (int64, error) {
	name = strings.TrimSpace(name)
	c = c.Normalize()
	fe := c.Validate()
	if fe == nil {
		fe = store.FieldErrors{}
	}
	var tested *Test
	if testID != 0 {
		if t, err := s.Test(ctx, testID); err == nil && t.RouterID == 0 && t.Conn == c && (t.State == TestPassed || t.State == TestWarned) {
			tested = &t
		}
	}
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := checkName(ctx, tx, name, 0, fe); err != nil {
			return err
		}
		l, err := store.GetList(ctx, tx, listID)
		if errors.Is(err, store.ErrNotFound) {
			fe["list"] = "routers.err.list"
		} else if err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		now := s.now()
		row := store.Router{
			Name: name, ListID: listID, State: "active", Host: c.Host, Port: c.Port, User: c.User, JumpHost: c.JumpHost,
			JumpPort: c.JumpPort, JumpUser: c.JumpUser, Tailnet: c.Tailnet, AddressList: c.Names.List, Forwarder: c.Names.Forwarder,
			CreatedBy: actor, CreatedAt: now,
		}
		if tested != nil {
			row.Version, row.Board, row.ConnectedAt, row.LastSeenAt = tested.Version, tested.Board, now, now
		}
		if id, err = store.InsertRouter(ctx, tx, row); err != nil {
			return err
		}
		if err := s.record(ctx, tx, "routing.router_added", id, actor, map[string]any{"list": l.Name, "host": c.Host, "by": actor}); err != nil {
			return err
		}
		if tested == nil {
			return nil
		}
		if err := s.record(ctx, tx, "routing.router_connected", id, actor, map[string]any{"version": tested.Version, "board": tested.Board}); err != nil {
			return err
		}
		_, err = s.enqueueSync(ctx, tx, id, syncPayload{Trigger: TriggerInitial}, 0, actor)
		return err
	})
	if err != nil {
		return 0, err
	}
	// The add form pinned the router's key as router:new: it is this one's now.
	if err := s.d.SSH.SetSubject(ctx, c.Address(), RouterSubject(id)); err != nil && !errors.Is(err, sshx.ErrNotFound) {
		s.d.Log.Warn("routers: known host subject", "router", id, "error", err)
	}
	s.d.Jobs.Kick()
	return id, nil
}

// Edit changes a router's name, connection and names. A new address is
// confirmed by the next test; new names mark the router for a sync.
func (s *Service) Edit(ctx context.Context, id int64, name string, c Connection, actor string) (bool, error) {
	name = strings.TrimSpace(name)
	c = c.Normalize()
	fe := c.Validate()
	if fe == nil {
		fe = store.FieldErrors{}
	}
	changed := false
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetRouter(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := checkName(ctx, tx, name, id, fe); err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		old := connOf(cur)
		var changes []string
		if name != cur.Name {
			changes = append(changes, "name")
		}
		oldConn, newConn := old, c
		oldConn.Names, newConn.Names = c.Names, c.Names
		if oldConn != newConn {
			changes = append(changes, "connection")
		}
		if old.Names != c.Names {
			changes = append(changes, "names")
		}
		if len(changes) == 0 {
			return nil
		}
		changed = true
		row := cur
		row.Name, row.Host, row.Port, row.User, row.JumpHost, row.JumpPort, row.JumpUser, row.Tailnet =
			name, c.Host, c.Port, c.User, c.JumpHost, c.JumpPort, c.JumpUser, c.Tailnet
		row.AddressList, row.Forwarder = c.Names.List, c.Names.Forwarder
		if err := store.SetRouterConnection(ctx, tx, row); err != nil {
			return err
		}
		payload := map[string]any{"changes": strings.Join(changes, ", ")}
		if name != cur.Name {
			payload["from"], payload["to"] = cur.Name, name
		}
		if err := s.record(ctx, tx, "routing.router_updated", id, actor, payload); err != nil {
			return err
		}
		if old.Names != c.Names {
			return s.Marker().Mark(ctx, tx, markChange([]int64{id}, actor, name+" names changed"))
		}
		return nil
	})
	return changed, err
}

// SetList makes the router follow another list and marks it for a sync.
func (s *Service) SetList(ctx context.Context, id, listID int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetRouter(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		l, err := store.GetList(ctx, tx, listID)
		if errors.Is(err, store.ErrNotFound) {
			return store.FieldErrors{"list": "routers.err.list"}
		} else if err != nil {
			return err
		}
		if cur.ListID == listID {
			return nil
		}
		if err := store.MoveRouter(ctx, tx, id, listID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, "routing.router_updated", id, actor, map[string]any{"changes": "list", "from": cur.List, "to": l.Name}); err != nil {
			return err
		}
		return s.Marker().Mark(ctx, tx, markChange([]int64{id}, actor, cur.Name+" switched to "+l.Name))
	})
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

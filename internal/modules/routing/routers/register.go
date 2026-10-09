package routers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// AwaitingFor is how long a registered router is probed for its first contact.
const AwaitingFor = 7 * 24 * time.Hour

// Registration is a router the router script will set up (Phase 4).
type Registration struct {
	Name   string
	ListID int64 // 0: the default list
	Conn   Connection
}

// Registered is a registered router and the names its script must use.
type Registered struct {
	ID    int64
	Names routeros.Names
}

// Register saves a router in awaiting setup: no test runs, it gets no sync
// from changes and never notifies; the probe connects it within AwaitingFor.
// Name, list and connection are validated as Add router's (store.FieldErrors).
// Phase 4's RouterRegistrar wraps it.
func (s *Service) Register(ctx context.Context, r Registration, by string) (Registered, error) {
	name := strings.TrimSpace(r.Name)
	c := r.Conn.Normalize()
	fe := c.Validate()
	if fe == nil {
		fe = store.FieldErrors{}
	}
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := checkName(ctx, tx, name, 0, fe); err != nil {
			return err
		}
		var l store.List
		var err error
		if r.ListID == 0 {
			l, err = store.DefaultList(ctx, tx)
		} else {
			l, err = store.GetList(ctx, tx, r.ListID)
		}
		if errors.Is(err, store.ErrNotFound) {
			fe["list"] = "routers.err.list"
		} else if err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		now := s.now()
		if id, err = store.InsertRouter(ctx, tx, store.Router{
			Name: name, ListID: l.ID, State: StateAwaiting, Host: c.Host, Port: c.Port, User: c.User, JumpHost: c.JumpHost,
			JumpPort: c.JumpPort, JumpUser: c.JumpUser, Tailnet: c.Tailnet, AddressList: c.Names.List, Forwarder: c.Names.Forwarder,
			AwaitingUntil: db.At(s.d.Now().Add(AwaitingFor)), CreatedBy: by, CreatedAt: now,
		}); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.router_added", id, by, map[string]any{"list": l.Name, "host": c.Host, "by": by})
	})
	if err != nil {
		return Registered{}, err
	}
	return Registered{ID: id, Names: c.Names}, nil
}

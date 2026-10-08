// Package lists manages routing lists: the lists, their services in order,
// the effective view targets get (ownership, ADR 0013), the server-hostname
// guard and its port for provisioning, and warnings
// (docs/processes/routing/routing-lists.md).
package lists

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// What the service refuses, besides store.FieldErrors, *GuardError and
// *HasTargetsError.
var (
	ErrNotFound   = errors.New("lists: no such list")
	ErrDefault    = errors.New("lists: the default list can't be deleted")
	ErrStaleOrder = errors.New("lists: the services changed meanwhile")
	ErrNotMember  = errors.New("lists: the service is not in the list")
	ErrMoveTo     = errors.New("lists: the targets must move to another list")
)

// Limits of a list's fields.
const (
	MaxName        = 60
	MaxDescription = 1000
)

// GuardError: a save would put a name covering a server's hostname into a
// list. Nothing was saved.
type GuardError struct {
	List, Service, Domain, Server, Hostname string
	// ListIDs are the lists the save would have broken: one refusal is
	// recorded for each.
	ListIDs []int64
}

func (e *GuardError) Error() string {
	return fmt.Sprintf("lists: %s (%s) in %s would cover %s, the hostname of %s", e.Domain, e.Service, e.List, e.Hostname, e.Server)
}

// HasTargetsError: routers or Shadowrocket configs follow the list; they
// must move to another list in the same step.
type HasTargetsError struct{ Targets []string } // "Home (router)", "iphone (Shadowrocket)"

func (e *HasTargetsError) Error() string {
	return "lists: followed by " + strings.Join(e.Targets, ", ")
}

// List is a routing list.
type List struct {
	ID          int64
	Name        string
	Description string
	Default     bool
	CreatedAt   time.Time
}

// Row is a line of the lists page.
type Row struct {
	List
	Services int
	Domains  int      // owned names
	Targets  []Target // 3d, 3e
	Warnings int
}

// Target is a router or a Shadowrocket config following a list.
type Target struct {
	Kind  string // router, shadowrocket
	ID    int64
	Name  string
	State string // the target's status word from its row; 3d and 3e word it
}

// Membership is a list holding a service, and where.
type Membership struct {
	List     List
	Position int
}

// Deps are what the service uses.
type Deps struct {
	DB        *db.DB
	Events    *events.Catalog
	Services  *services.Service
	Hostnames servers.ServerHostnames // may be nil: nothing to guard
	Marker    change.Marker
	Now       func() time.Time
	Log       *slog.Logger
}

// Service manages routing lists.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Marker == nil {
		d.Marker = change.None{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

// Subject is a list's event subject.
func Subject(id int64) events.Subject {
	return events.Subject{Type: "routing_list", ID: strconv.FormatInt(id, 10)}
}

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, subject events.Subject, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: db.At(s.d.Now()), Type: typ, Subject: subject, Actor: actor, Payload: payload})
	return err
}

func toList(l store.List) List {
	return List{ID: l.ID, Name: l.Name, Description: l.Description, Default: l.IsDefault, CreatedAt: l.CreatedAt.Time}
}

func getList(ctx context.Context, q sqlx.QueryerContext, id int64) (store.List, error) {
	l, err := store.GetList(ctx, q, id)
	if errors.Is(err, store.ErrNotFound) {
		return l, ErrNotFound
	}
	return l, err
}

// Get reads a list.
func (s *Service) Get(ctx context.Context, id int64) (List, error) {
	l, err := getList(ctx, s.d.DB.R, id)
	return toList(l), err
}

// Default reads the default list.
func (s *Service) Default(ctx context.Context) (List, error) {
	l, err := store.DefaultList(ctx, s.d.DB.R)
	if errors.Is(err, store.ErrNotFound) {
		return List{}, ErrNotFound
	}
	return toList(l), err
}

// All reads every list: the default first, then by name.
func (s *Service) All(ctx context.Context) ([]List, error) {
	rows, err := store.Lists(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	out := make([]List, 0, len(rows))
	for _, l := range rows {
		out = append(out, toList(l))
	}
	return out, nil
}

// List reads the lists page: each list with its counts, targets and warnings.
func (s *Service) List(ctx context.Context) ([]Row, error) {
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(all))
	for _, l := range all {
		v, err := s.View(ctx, l.ID, nil)
		if err != nil {
			return nil, err
		}
		t, err := s.Targets(ctx, l.ID)
		if err != nil {
			return nil, err
		}
		w, err := s.warnings(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, Row{List: l, Services: len(v.Members), Domains: v.Result.Count(), Targets: t, Warnings: len(w)})
	}
	return out, nil
}

// fields checks a list's name and description against the other lists.
func fields(ctx context.Context, q sqlx.QueryerContext, id int64, name, description string) (store.FieldErrors, error) {
	fe := store.FieldErrors{}
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxName {
		fe["name"] = "lists.err.name"
	} else if taken, err := store.ListNameTaken(ctx, q, name, id); err != nil {
		return nil, err
	} else if taken {
		fe["name"] = "lists.err.name_taken"
	}
	if utf8.RuneCountInString(description) > MaxDescription {
		fe["description"] = "lists.err.description"
	}
	return fe, nil
}

// Create makes an empty list (store.FieldErrors when refused).
func (s *Service) Create(ctx context.Context, name, description, actor string) (int64, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		fe, err := fields(ctx, tx, 0, name, description)
		if err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		if id, err = store.InsertList(ctx, tx, store.List{Name: name, Description: description, CreatedAt: db.At(s.d.Now())}); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.list_created", Subject(id), actor, map[string]any{"name": name})
	})
	return id, err
}

// Edit renames and describes a list; unchanged records nothing. A name
// changes nothing targets get, so nothing is marked.
func (s *Service) Edit(ctx context.Context, id int64, name, description, actor string) (bool, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	changed := false
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := getList(ctx, tx, id)
		if err != nil {
			return err
		}
		fe, err := fields(ctx, tx, id, name, description)
		if err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		var changes []string
		payload := map[string]any{}
		if name != cur.Name {
			changes = append(changes, "name")
			payload["from"], payload["to"] = cur.Name, name
		}
		if description != cur.Description {
			changes = append(changes, "description")
		}
		if len(changes) == 0 {
			return nil
		}
		changed = true
		if err := store.SetListFields(ctx, tx, id, name, description); err != nil {
			return err
		}
		payload["changes"] = strings.Join(changes, ", ")
		return s.record(ctx, tx, "routing.list_updated", Subject(id), actor, payload)
	})
	return changed, err
}

// MakeDefault moves the default mark to the list; on the default it changes nothing.
func (s *Service) MakeDefault(ctx context.Context, id int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := getList(ctx, tx, id)
		if err != nil || cur.IsDefault {
			return err
		}
		if err := store.MakeDefault(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.list_updated", Subject(id), actor, map[string]any{"changes": "default"})
	})
}

// ListsOf reads the lists that hold a service, by name.
func (s *Service) ListsOf(ctx context.Context, serviceID int64) ([]List, error) {
	ms, err := s.Memberships(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make([]List, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.List)
	}
	return out, nil
}

// Memberships reads the lists that hold a service with its position in each, by name.
func (s *Service) Memberships(ctx context.Context, serviceID int64) ([]Membership, error) {
	rows, err := store.MembershipsOf(ctx, s.d.DB.R, serviceID)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, 0, len(rows))
	for _, r := range rows {
		out = append(out, Membership{List: List{ID: r.ListID, Name: r.Name, Default: r.IsDefault}, Position: r.Position})
	}
	return out, nil
}

// Targets reads the routers and Shadowrocket configs following a list.
func (s *Service) Targets(ctx context.Context, id int64) ([]Target, error) {
	rows, err := store.ListTargets(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		out = append(out, Target(r))
	}
	return out, nil
}

// Size is the number of services in a list.
func (s *Service) Size(ctx context.Context, id int64) (int, error) {
	ms, err := store.Members(ctx, s.d.DB.R, id)
	return len(ms), err
}

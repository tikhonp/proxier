// Package services manages services: adding upstream ones, switching their
// source, custom services and their domain editor, removing; their snapshots
// and history (docs/processes/routing/service-management.md).
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// What the service refuses, besides store.FieldErrors and the errors of
// selector and sources.
var (
	ErrNotFound     = errors.New("services: no such service")
	ErrCustom       = errors.New("services: a custom service has no source")
	ErrNotCustom    = errors.New("services: not a custom service")
	ErrEmptyResolve = errors.New("services: resolves to no usable names")
)

// TagTakenError: another service has the selector's tag. Nothing was resolved.
type TagTakenError struct {
	Tag      string
	Existing Item
}

func (e *TagTakenError) Error() string {
	return fmt.Sprintf("services: tag %s is taken by %s", e.Tag, e.Existing.Selector)
}

// SwitchTagError: a switch keeps the tag, and the new selector has another.
type SwitchTagError struct{ Want, Got string }

func (e *SwitchTagError) Error() string {
	return fmt.Sprintf("services: a switch keeps the tag: the selector has tag %s, not %s", e.Got, e.Want)
}

// InListsError: a service is removed only when no list holds it.
type InListsError struct{ Lists []string }

func (e *InListsError) Error() string { return "services: in " + strings.Join(e.Lists, ", ") }

// EmptyResolveError: the selector resolved to nothing RouterOS can use.
type EmptyResolveError struct {
	Selector string
	Skipped  int
}

func (e *EmptyResolveError) Error() string {
	return fmt.Sprintf("%s has no names RouterOS can use (%d skipped)", e.Selector, e.Skipped)
}

func (e *EmptyResolveError) Is(target error) bool { return target == ErrEmptyResolve }

// Item is one service as stored.
type Item struct {
	ID          int64
	Tag         string
	Source      selector.Source
	Selector    string
	Name        string // custom
	Description string
	Origin      string
	LastChecked time.Time
	LastError   string
	Failures    int
	CreatedAt   time.Time
}

// Snapshot is one snapshot of a service. History rows carry no names (Set is
// empty): their sizes are SuffixCount and ExactCount.
type Snapshot struct {
	ID                      int64
	ServiceID               int64
	Status                  string
	Selector                string
	Portal                  string
	Kind                    string
	Set                     snapshot.Set
	SuffixCount, ExactCount int
	Added, Removed          int
	Reason                  string
	LostPct                 int
	ForcedBy                string
	InRound                 bool
	Dismissed               time.Time
	FetchedAt               time.Time
	AcceptedAt              time.Time
}

// Count is the number of names.
func (s Snapshot) Count() int { return s.SuffixCount + s.ExactCount }

// Row is one line of the services list.
type Row struct {
	Item
	Count   int      // names in the accepted snapshot
	Suffix  int      // of which suffix
	Exact   int      // and exact
	State   string   // ok (3c adds waiting, failing)
	Lists   []string // the lists that hold it (3b fills them)
	SavedAt time.Time
}

// Filter narrows the services list. Pages slice it into pages.
type Filter struct {
	Source string // "" any
	List   int64  // services in that list
	NoList bool   // in no list
	State  string // 3c: ok, waiting, failing
	Page   int
}

// Deps are what the service uses.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Resolver *sources.Resolver
	Marker   change.Marker
	Now      func() time.Time
	Log      *slog.Logger
	// Check refuses a custom service's names inside its save's transaction:
	// the server-hostname guard of the lists holding it (lists.CheckService).
	Check func(ctx context.Context, tx *sqlx.Tx, serviceID int64, s snapshot.Set) error
	// Refused is told about a save Check refused, after its transaction rolled
	// back, so the refusal can be recorded (lists.Refused).
	Refused func(ctx context.Context, err error, actor string)
}

// Service manages services.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Marker == nil {
		d.Marker = change.None{}
	}
	return &Service{d: d}
}

// Interactive is how long a resolve inside an admin request may take overall.
const Interactive = 45 * time.Second

func item(s store.Service) Item {
	return Item{
		ID: s.ID, Tag: s.Tag, Source: selector.Source(s.Source), Selector: s.Selector, Name: s.Name, Description: s.Description,
		Origin: s.Origin, LastChecked: s.LastCheckedAt.Time, LastError: s.LastError, Failures: s.Failures, CreatedAt: s.CreatedAt.Time,
	}
}

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, id int64, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{
		Time: db.At(s.d.Now()), Type: typ, Subject: Subject(id), Actor: actor, Payload: payload,
	})
	return err
}

// Subject is a service's event subject.
func Subject(id int64) events.Subject { return events.Subject{Type: "service", ID: fmt.Sprint(id)} }

// Get reads a service.
func (s *Service) Get(ctx context.Context, id int64) (Item, error) {
	r, err := store.GetService(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Item{}, ErrNotFound
	}
	return item(r), err
}

// ByTag reads a service by its tag.
func (s *Service) ByTag(ctx context.Context, tag string) (Item, error) {
	r, err := store.ServiceByTag(ctx, s.d.DB.R, tag)
	if errors.Is(err, store.ErrNotFound) {
		return Item{}, ErrNotFound
	}
	return item(r), err
}

// List reads the services the filter keeps, by tag.
func (s *Service) List(ctx context.Context, f Filter) ([]Row, error) {
	rows, err := store.ListServices(ctx, s.d.DB.R, f.Source)
	if err != nil {
		return nil, err
	}
	var in map[int64]bool
	if f.List != 0 {
		if in, err = store.ServicesInList(ctx, s.d.DB.R, f.List); err != nil {
			return nil, err
		}
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if in != nil && !in[r.ID] {
			continue
		}
		lists, err := store.ListsOf(ctx, s.d.DB.R, r.ID)
		if err != nil {
			return nil, err
		}
		if f.NoList && len(lists) > 0 {
			continue
		}
		out = append(out, Row{
			Item: item(r.Service), Count: r.SuffixCount + r.ExactCount, Suffix: r.SuffixCount, Exact: r.ExactCount,
			State: "ok", Lists: lists, SavedAt: r.AcceptedAt.Time,
		})
	}
	return out, nil
}

// Counts are the services list's status line.
func (s *Service) Counts(ctx context.Context) (all, custom int, err error) {
	return store.CountServices(ctx, s.d.DB.R)
}

// ListsOf names the lists that hold a service.
func (s *Service) ListsOf(ctx context.Context, id int64) ([]string, error) {
	return store.ListsOf(ctx, s.d.DB.R, id)
}

func toSnapshot(r store.Snapshot) (Snapshot, error) {
	var skipped []snapshot.Skipped
	if r.Skipped != "" && r.Skipped != "[]" {
		if err := json.Unmarshal([]byte(r.Skipped), &skipped); err != nil {
			return Snapshot{}, fmt.Errorf("services: snapshot %d: %w", r.ID, err)
		}
	}
	return Snapshot{
		ID: r.ID, ServiceID: r.ServiceID, Status: r.Status, Selector: r.Selector, Portal: r.Portal, Kind: r.Kind,
		Set:         snapshot.Set{Suffix: snapshot.Decode(r.Suffix), Exact: snapshot.Decode(r.Exact), Skipped: skipped},
		SuffixCount: r.SuffixCount, ExactCount: r.ExactCount, Added: r.Added, Removed: r.Removed, Reason: r.Reason,
		LostPct: r.LostPct, ForcedBy: r.ForcedBy, InRound: r.InRound, Dismissed: r.DismissedAt.Time,
		FetchedAt: r.FetchedAt.Time, AcceptedAt: r.AcceptedAt.Time,
	}, nil
}

// Accepted reads a service's accepted snapshot with its names.
func (s *Service) Accepted(ctx context.Context, id int64) (Snapshot, error) {
	r, err := store.AcceptedSnapshot(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	return toSnapshot(r)
}

// Snapshots lists a service's snapshots without their names, newest first.
func (s *Service) Snapshots(ctx context.Context, id int64) ([]Snapshot, error) {
	rows, err := store.SnapshotHistory(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(rows))
	for _, r := range rows {
		sn, err := toSnapshot(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, nil
}

// Snapshot reads one snapshot and the snapshot accepted before it (zero for
// the first), both with their names.
func (s *Service) Snapshot(ctx context.Context, id, snap int64) (Snapshot, Snapshot, error) {
	r, err := store.GetSnapshot(ctx, s.d.DB.R, id, snap)
	if errors.Is(err, store.ErrNotFound) {
		return Snapshot{}, Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, Snapshot{}, err
	}
	it, err := toSnapshot(r)
	if err != nil {
		return Snapshot{}, Snapshot{}, err
	}
	p, err := store.AcceptedBefore(ctx, s.d.DB.R, id, snap)
	if errors.Is(err, store.ErrNotFound) {
		return it, Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, Snapshot{}, err
	}
	prev, err := toSnapshot(p)
	return it, prev, err
}

// CustomRows reads a custom service's editable rows, by domain.
func (s *Service) CustomRows(ctx context.Context, id int64) ([]DomainRow, error) {
	rows, err := store.CustomDomains(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make([]DomainRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, DomainRow{Domain: r.Domain, Exact: r.Exact, Note: r.Note})
	}
	return out, nil
}

// newSnapshot is the stored form of a set; old is the accepted set it is
// compared with (nil for the first one).
func (s *Service) newSnapshot(serviceID int64, sel, portal, kind string, set snapshot.Set, old *snapshot.Set) (store.Snapshot, error) {
	skipped := []byte("[]")
	if len(set.Skipped) > 0 {
		var err error
		if skipped, err = json.Marshal(set.Skipped); err != nil {
			return store.Snapshot{}, err
		}
	}
	now := db.At(s.d.Now())
	r := store.Snapshot{
		ServiceID: serviceID, Status: "accepted", Selector: sel, Portal: portal, Kind: kind,
		Suffix: snapshot.Encode(set.Suffix), Exact: snapshot.Encode(set.Exact), Skipped: string(skipped),
		SuffixCount: len(set.Suffix), ExactCount: len(set.Exact), Hash: set.Hash(), FetchedAt: now, AcceptedAt: now,
	}
	if old != nil {
		d := snapshot.Compare(*old, set)
		r.Added, r.Removed = d.Added(), d.Removed()
	}
	return r, nil
}

func acceptedSet(ctx context.Context, q sqlx.QueryerContext, id int64) (snapshot.Set, string, error) {
	a, err := store.AcceptedSnapshot(ctx, q, id)
	if err != nil {
		return snapshot.Set{}, "", err
	}
	return snapshot.Set{Suffix: snapshot.Decode(a.Suffix), Exact: snapshot.Decode(a.Exact)}, a.Hash, nil
}

// display is a name with its Unicode form, for reports.
func display(d string) domain.Name { return domain.Name{Domain: d, Unicode: domain.Unicode(d)} }

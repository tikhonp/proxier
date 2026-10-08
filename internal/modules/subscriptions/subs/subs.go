// Package subs is the subscriptions themselves: create, settings, their
// servers and order, the preview, delete, the reactions to the servers
// module's events and the usage port it reads
// (docs/processes/subscriptions/subscription-management.md).
package subs

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

var (
	ErrNotFound   = errors.New("subs: no such subscription")
	ErrHasLinks   = errors.New("subs: links still point to the subscription")
	ErrNotServed  = errors.New("subs: the server is not active")
	ErrStaleOrder = errors.New("subs: the servers changed meanwhile")
)

// Bounds of the settings form.
const (
	MaxName        = 60
	MaxTitle       = 60
	MaxDescription = 1000
	MinUpdateHours = 1
	MaxUpdateHours = 168
	MaxGraceMin    = 10080
)

// Subscription is a subscription's settings.
type Subscription struct {
	ID                       int64
	Name, Title, Description string
	Formats                  []string
	DefaultFormat            string
	UpdateHours              int
	Hide                     output.Hide
	AutoAdd                  bool
	AutoAddSince             time.Time // zero when off
	CreatedAt                time.Time
}

// Settings is the settings form.
type Settings struct {
	Name, Title, Description string
	Formats                  []string
	DefaultFormat            string
	UpdateHours              int
	HideOn                   bool
	HideStates               []string
	GraceMinutes             int
	AutoAdd                  bool
}

// Member is a server of a subscription, in service or not.
type Member struct {
	ServerID  int64
	Name      string // the copy stored when it was added
	Flag      string // "" when not in service
	Position  int
	InService bool
	Server    output.Server // when InService
}

// Row is a subscription on the list page.
type Row struct {
	Subscription
	Members []Member
	Links   int // not deleted
}

// Deps are what the service uses.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Settings *settings.Store
	I18n     *i18n.Catalog
	Catalog  servers.EndpointCatalog // may be nil: no server is in service
	Now      func() time.Time
	Log      *slog.Logger
}

// Service runs subscriptions.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// HasCatalog reports whether servers can be added at all.
func (s *Service) HasCatalog() bool { return s.d.Catalog != nil }

// Subject is a subscription's event subject.
func Subject(id int64) events.Subject {
	return events.Subject{Type: "subscription", ID: strconv.FormatInt(id, 10)}
}

func (s *Service) now() db.Time { return db.At(s.d.Now()) }

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, id int64, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: s.now(), Type: typ, Subject: Subject(id), Actor: actor, Payload: payload})
	return err
}

func fromRow(r store.Subscription) Subscription {
	return Subscription{
		ID: r.ID, Name: r.Name, Title: r.Title, Description: r.Description,
		Formats: split(r.Formats), DefaultFormat: r.DefaultFormat, UpdateHours: r.UpdateHours,
		Hide:    output.Hide{On: r.HideUnhealthy, States: split(r.HideStates), Grace: time.Duration(r.HideGraceMin) * time.Minute},
		AutoAdd: r.AutoAdd, AutoAddSince: r.AutoAddSince.Time, CreatedAt: r.CreatedAt.Time,
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// inOrder keeps the values of picked that are in order, in order's order.
func inOrder(order, picked []string) []string {
	var out []string
	for _, o := range order {
		if slices.Contains(picked, o) {
			out = append(out, o)
		}
	}
	return out
}

func checkNames(fe store.FieldErrors, name, title, description string) {
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		fe["name"] = "subs.err.name_required"
	case n > MaxName:
		fe["name"] = "subs.err.name_long"
	}
	if utf8.RuneCountInString(title) > MaxTitle {
		fe["title"] = "subs.err.title_long"
	}
	if utf8.RuneCountInString(description) > MaxDescription {
		fe["description"] = "subs.err.description_long"
	}
}

// Create adds a subscription with the default settings. An empty title is
// the name.
func (s *Service) Create(ctx context.Context, name, title, description, actor string) (int64, error) {
	name, title, description = strings.TrimSpace(name), strings.TrimSpace(title), strings.TrimSpace(description)
	if title == "" {
		title = name
	}
	fe := store.FieldErrors{}
	checkNames(fe, name, title, description)
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if fe["name"] == "" {
			if taken, err := store.NameTaken(ctx, tx, name, 0); err != nil {
				return err
			} else if taken {
				fe["name"] = "subs.err.name_taken"
			}
		}
		if len(fe) > 0 {
			return fe
		}
		var err error
		if id, err = store.InsertSubscription(ctx, tx, name, title, description, s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, "subscription.created", id, actor, map[string]any{"name": name})
	})
	return id, err
}

// Get reads one subscription.
func (s *Service) Get(ctx context.Context, id int64) (Subscription, error) {
	r, err := store.GetSubscription(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Subscription{}, ErrNotFound
	}
	return fromRow(r), err
}

// inService is what the catalog serves now, by server id.
func (s *Service) inService(ctx context.Context) (map[int64]servers.ServerEndpoints, error) {
	out := map[int64]servers.ServerEndpoints{}
	if s.d.Catalog == nil {
		return out, nil
	}
	list, err := s.d.Catalog.Active(ctx)
	if err != nil {
		return nil, err
	}
	for _, se := range list {
		out[se.ServerID] = se
	}
	return out, nil
}

func member(m store.Member, active map[int64]servers.ServerEndpoints) Member {
	out := Member{ServerID: m.ServerID, Name: m.ServerName, Position: m.Position}
	if se, ok := active[m.ServerID]; ok {
		out.InService, out.Flag = true, se.Flag
		out.Server = output.Server{ID: se.ServerID, Name: se.Name, Health: se.Health, HealthSince: se.HealthSince, Endpoints: se.Endpoints}
	}
	return out
}

// List is every subscription with its members and live links, by name.
func (s *Service) List(ctx context.Context) ([]Row, error) {
	all, err := store.ListSubscriptions(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	members, err := store.AllMembers(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	links, err := store.LinkCounts(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	active, err := s.inService(ctx)
	if err != nil {
		return nil, err
	}
	bySub := map[int64][]Member{}
	for _, m := range members {
		bySub[m.SubscriptionID] = append(bySub[m.SubscriptionID], member(m, active))
	}
	out := make([]Row, len(all))
	for i, r := range all {
		out[i] = Row{Subscription: fromRow(r), Members: bySub[r.ID], Links: links[r.ID]}
	}
	return out, nil
}

// Members of a subscription, in order, with what the catalog serves now.
func (s *Service) Members(ctx context.Context, id int64) ([]Member, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := store.Members(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	active, err := s.inService(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Member, len(rows))
	for i, m := range rows {
		out[i] = member(m, active)
	}
	return out, nil
}

// Update saves the settings form. changed is false when nothing differed;
// then nothing is recorded.
func (s *Service) Update(ctx context.Context, id int64, st Settings, actor string) (bool, error) {
	st.Name, st.Title, st.Description = strings.TrimSpace(st.Name), strings.TrimSpace(st.Title), strings.TrimSpace(st.Description)
	if st.Title == "" {
		st.Title = st.Name
	}
	fe := store.FieldErrors{}
	checkNames(fe, st.Name, st.Title, st.Description)
	formats := inOrder(output.Names(), st.Formats)
	switch {
	case len(formats) == 0:
		fe["formats"] = "subs.err.no_format"
	case !slices.Contains(formats, st.DefaultFormat):
		fe["default_format"] = "subs.err.default_not_allowed"
	}
	if st.UpdateHours < MinUpdateHours || st.UpdateHours > MaxUpdateHours {
		fe["update_hours"] = "subs.err.update_hours"
	}
	states := inOrder(output.HideStates, st.HideStates)
	if st.HideOn && len(states) == 0 {
		fe["hide_states"] = "subs.err.hide_states"
	}
	if st.GraceMinutes < 0 || st.GraceMinutes > MaxGraceMin {
		fe["grace"] = "subs.err.grace"
	}
	changed := false
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		old, err := store.GetSubscription(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if fe["name"] == "" {
			if taken, err := store.NameTaken(ctx, tx, st.Name, id); err != nil {
				return err
			} else if taken {
				fe["name"] = "subs.err.name_taken"
			}
		}
		if len(fe) > 0 {
			return fe
		}
		next := old
		next.Name, next.Title, next.Description = st.Name, st.Title, st.Description
		next.Formats, next.DefaultFormat, next.UpdateHours = strings.Join(formats, ","), st.DefaultFormat, st.UpdateHours
		next.HideUnhealthy, next.HideGraceMin = st.HideOn, st.GraceMinutes
		if len(states) > 0 { // turned off with nothing ticked keeps the states for next time
			next.HideStates = strings.Join(states, ",")
		}
		next.AutoAdd = st.AutoAdd
		switch {
		case st.AutoAdd && !old.AutoAdd:
			next.AutoAddSince = s.now()
		case !st.AutoAdd:
			next.AutoAddSince = db.Time{}
		}
		var changes []string
		for _, c := range []struct {
			what string
			diff bool
		}{
			{"name", old.Name != next.Name},
			{"title", old.Title != next.Title},
			{"description", old.Description != next.Description},
			{"formats", old.Formats != next.Formats},
			{"default format", old.DefaultFormat != next.DefaultFormat},
			{"update interval", old.UpdateHours != next.UpdateHours},
			{"hide unhealthy", old.HideUnhealthy != next.HideUnhealthy},
			{"hidden states", old.HideStates != next.HideStates},
			{"grace", old.HideGraceMin != next.HideGraceMin},
			{"auto add", old.AutoAdd != next.AutoAdd},
		} {
			if c.diff {
				changes = append(changes, c.what)
			}
		}
		if len(changes) == 0 {
			return nil
		}
		if err := store.UpdateSubscription(ctx, tx, next); err != nil {
			if store.Unique(err, "subs_subscriptions.name") {
				return store.FieldErrors{"name": "subs.err.name_taken"}
			}
			return err
		}
		changed = true
		return s.record(ctx, tx, "subscription.updated", id, actor, map[string]any{"changes": strings.Join(changes, ", ")})
	})
	return changed, err
}

// Holder is a link that keeps a subscription from being deleted.
type Holder struct {
	Name  string
	State string    // active, disabled, deleted
	Ends  time.Time // deleted: when its tombstone ends
}

// holders: the live links, and the deleted ones still inside the tombstone
// period in force now.
func (s *Service) holders(ctx context.Context, q sqlx.QueryerContext, id int64) ([]Holder, error) {
	tomb, err := s.d.Settings.GetDuration(ctx, conf.Tombstone)
	if err != nil {
		return nil, err
	}
	links, err := store.LinksOf(ctx, q, id)
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	var out []Holder
	for _, l := range links {
		if l.State != "deleted" {
			out = append(out, Holder{Name: l.Name, State: l.State})
			continue
		}
		if ends := l.DeletedAt.Add(tomb); ends.After(now) {
			out = append(out, Holder{Name: l.Name, State: l.State, Ends: ends})
		}
	}
	return out, nil
}

// Holders are the links that keep the subscription from being deleted now.
func (s *Service) Holders(ctx context.Context, id int64) ([]Holder, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	return s.holders(ctx, s.d.DB.R, id)
}

// Delete removes a subscription no link holds; ErrHasLinks otherwise.
func (s *Service) Delete(ctx context.Context, id int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		sub, err := store.GetSubscription(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		h, err := s.holders(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(h) > 0 {
			return ErrHasLinks
		}
		if err := store.DeleteSubscription(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "subscription.deleted", id, actor, map[string]any{"name": sub.Name})
	})
}

// Usage names the subscriptions (sorted) holding the server and counts their
// active, unexpired links.
func (s *Service) Usage(ctx context.Context, serverID int64) ([]string, int, error) {
	ids, err := store.SubscriptionsOfServer(ctx, s.d.DB.R, serverID)
	if err != nil {
		return nil, 0, err
	}
	var names []string
	for _, id := range ids {
		sub, err := store.GetSubscription(ctx, s.d.DB.R, id)
		if err != nil {
			return nil, 0, err
		}
		names = append(names, sub.Name)
	}
	slices.Sort(names)
	n, err := store.ActiveLinks(ctx, s.d.DB.R, ids, s.now())
	return names, n, err
}

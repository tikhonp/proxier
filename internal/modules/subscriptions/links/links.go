// Package links is what a person or device is handed: a named secret URL
// serving one subscription. It creates links, runs every action on them,
// keeps the token sealed, and decides what a link serves now (Serve)
// (docs/processes/subscriptions/link-lifecycle.md).
package links

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

var (
	ErrNotFound = errors.New("links: no such link")
	ErrDeleted  = errors.New("links: the link is deleted")
)

// Bounds of the forms.
const (
	MaxName = 80
	MaxNote = 1000
)

// Link is a link without its token.
type Link struct {
	ID             int64
	SubscriptionID int64 // 0 only for a deleted link whose tombstone ended before its subscription was deleted
	Name, Note     string
	State          string    // active, disabled, deleted
	Expires        time.Time // the first moment it is expired; zero: never
	Lang           i18n.Lang
	Format         string // "" = the subscription's default
	// AlertNetworks and AlertApps override the settings when not 0 (2c).
	AlertNetworks, AlertApps int
	AlertsMuted              bool
	AlertedAt                time.Time
	CreatedAt, DisabledAt    time.Time
	DeletedAt, LastFetchAt   time.Time
	LastFetchApp             string
	LastFetchNetwork         string
}

// Expired: active or not, the expiry has passed.
func (l Link) Expired(now time.Time) bool { return !l.Expires.IsZero() && !now.Before(l.Expires) }

// Status is the shown state: active, disabled, expired or deleted.
func (l Link) Status(now time.Time) string {
	if l.State == "active" && l.Expired(now) {
		return "expired"
	}
	return l.State
}

// TombstoneEnds is when a deleted link stops serving "Link removed".
func (l Link) TombstoneEnds(period time.Duration) time.Time { return l.DeletedAt.Add(period) }

func fromRow(r store.Link) Link {
	return Link{
		ID: r.ID, SubscriptionID: r.SubscriptionID.Int64, Name: r.Name, Note: r.Note, State: r.State,
		Expires: r.ExpiresAt.Time, Lang: i18n.Lang(r.Language), Format: r.Format.String,
		AlertNetworks: int(r.AlertNetworks.Int64), AlertApps: int(r.AlertApps.Int64), AlertsMuted: r.AlertsMuted,
		AlertedAt: r.AlertedAt.Time, CreatedAt: r.CreatedAt.Time, DisabledAt: r.DisabledAt.Time,
		DeletedAt: r.DeletedAt.Time, LastFetchAt: r.LastFetchAt.Time,
		LastFetchApp: r.LastFetchApp, LastFetchNetwork: r.LastFetchNetwork,
	}
}

// New is the New link form.
type New struct {
	Name, Note     string
	SubscriptionID int64
	Expires        time.Time // zero: never
	Lang           i18n.Lang
}

// Edit is the Edit form.
type Edit struct {
	Name, Note string
	Lang       i18n.Lang
	Format     string // "" = the subscription's default
}

// Deps are what the service uses.
type Deps struct {
	DB       *db.DB
	Vault    *vault.Vault
	Events   *events.Catalog
	Settings *settings.Store
	I18n     *i18n.Catalog
	Subs     *subs.Service
	Now      func() time.Time
	BaseURL  *url.URL
	Log      *slog.Logger
	// Jobs and Rotator run cut-offs; a nil Rotator hides Cut off.
	Jobs    *jobs.System
	Rotator servers.Rotator
}

// Service runs links.
type Service struct{ d Deps }

// NewService returns the service (New is the form).
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// Subject is a link's event subject.
func Subject(id int64) events.Subject {
	return events.Subject{Type: "link", ID: strconv.FormatInt(id, 10)}
}

func tokenAAD(id int64) string { return "link:" + strconv.FormatInt(id, 10) + ":token" }

// timeText is a payload time: db.Time text, "" for none.
func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(db.TimeLayout)
}

func (s *Service) now() db.Time { return db.At(s.d.Now()) }

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, id int64, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: s.now(), Type: typ, Subject: Subject(id), Actor: actor, Payload: payload})
	return err
}

// Zone is the display time zone (general.time_zone); UTC when unreadable.
func (s *Service) Zone(ctx context.Context) *time.Location {
	if name, err := s.d.Settings.Get(ctx, "general.time_zone"); err == nil {
		if tz, err := time.LoadLocation(name); err == nil {
			return tz
		}
	}
	return time.UTC
}

// Tombstone is the period in force (subscriptions.tombstone).
func (s *Service) Tombstone(ctx context.Context) (time.Duration, error) {
	return s.d.Settings.GetDuration(ctx, conf.Tombstone)
}

// Ended reports whether a deleted link's URL answers 404 now: its tombstone
// passed, or it has no subscription left.
func (s *Service) Ended(ctx context.Context, l Link) (bool, error) {
	if l.State != "deleted" {
		return false, nil
	}
	if l.SubscriptionID == 0 {
		return true, nil
	}
	period, err := s.Tombstone(ctx)
	if err != nil {
		return false, err
	}
	return !s.d.Now().Before(l.TombstoneEnds(period)), nil
}

// DefaultLang is the language of new links (subscriptions.link_language).
func (s *Service) DefaultLang(ctx context.Context) i18n.Lang {
	if v, err := s.d.Settings.Get(ctx, conf.LinkLanguage); err == nil && i18n.Lang(v).Valid() {
		return i18n.Lang(v)
	}
	return i18n.RU
}

// URL is the link's public address.
func (s *Service) URL(token string) string {
	return strings.TrimSuffix(s.d.BaseURL.String(), "/") + "/s/" + token
}

func checkFields(fe store.FieldErrors, name, note string, lang i18n.Lang) {
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		fe["name"] = "links.err.name_required"
	case n > MaxName:
		fe["name"] = "links.err.name_long"
	}
	if utf8.RuneCountInString(note) > MaxNote {
		fe["note"] = "links.err.note_long"
	}
	if !lang.Valid() {
		fe["language"] = "links.err.language"
	}
}

func (s *Service) nameTaken(ctx context.Context, tx *sqlx.Tx, fe store.FieldErrors, name string, except int64) error {
	if fe["name"] != "" {
		return nil
	}
	taken, err := store.LinkNameTaken(ctx, tx, name, except)
	if taken {
		fe["name"] = "links.err.name_taken"
	}
	return err
}

// Create adds an active link with a new token. Errors are store.FieldErrors
// (nothing created).
func (s *Service) Create(ctx context.Context, n New, actor string) (int64, error) {
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		id, _, err = s.CreateTx(ctx, tx, n, actor)
		return err
	})
	return id, err
}

// CreateTx is Create inside the caller's transaction (router scripts' link
// issuer): the new link's id and token. Its field errors come before
// anything is written.
func (s *Service) CreateTx(ctx context.Context, tx *sqlx.Tx, n New, actor string) (int64, string, error) {
	n.Name, n.Note = strings.TrimSpace(n.Name), strings.TrimSpace(n.Note)
	fe := store.FieldErrors{}
	checkFields(fe, n.Name, n.Note, n.Lang)
	if !n.Expires.IsZero() && !n.Expires.After(s.d.Now()) {
		fe["expiry"] = "links.err.expiry_past"
	}
	sub, err := store.GetSubscription(ctx, tx, n.SubscriptionID)
	if errors.Is(err, store.ErrNotFound) {
		fe["subscription"] = "links.err.subscription"
	} else if err != nil {
		return 0, "", err
	}
	if err := s.nameTaken(ctx, tx, fe, n.Name, 0); err != nil {
		return 0, "", err
	}
	if len(fe) > 0 {
		return 0, "", fe
	}
	token := vault.NewToken()
	lookup := s.d.Vault.Lookup(token)
	row := store.Link{
		SubscriptionID: sql.NullInt64{Int64: n.SubscriptionID, Valid: true}, Name: n.Name, Note: n.Note,
		ExpiresAt: db.At(n.Expires), Language: string(n.Lang), CreatedAt: s.now(),
	}
	if n.Expires.IsZero() {
		row.ExpiresAt = db.Time{}
	}
	id, err := store.InsertLink(ctx, tx, row, lookup)
	if err != nil {
		if store.Unique(err, "subs_links.name") {
			return 0, "", store.FieldErrors{"name": "links.err.name_taken"}
		}
		return 0, "", err
	}
	if err := store.SetToken(ctx, tx, id, s.d.Vault.SealString(token, tokenAAD(id)), lookup); err != nil {
		return 0, "", err
	}
	if err := s.record(ctx, tx, "link.created", id, actor, map[string]any{"subscription": sub.Name, "expiry": timeText(n.Expires)}); err != nil {
		return 0, "", err
	}
	return id, token, nil
}

// Get reads one link.
func (s *Service) Get(ctx context.Context, id int64) (Link, error) {
	r, err := store.GetLink(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Link{}, ErrNotFound
	}
	return fromRow(r), err
}

// ByToken finds the link holding token; false when none does (never
// existed, regenerated, or erased).
func (s *Service) ByToken(ctx context.Context, token string) (Link, bool, error) {
	r, err := store.LinkByLookup(ctx, s.d.DB.R, s.d.Vault.Lookup(token))
	if errors.Is(err, store.ErrNotFound) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, err
	}
	return fromRow(r), true, nil
}

// Token opens a link's token. A deleted link has none to show (ErrDeleted).
func (s *Service) Token(ctx context.Context, id int64) (string, error) {
	l, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if l.State == "deleted" {
		return "", ErrDeleted
	}
	blob, err := store.Token(ctx, s.d.DB.R, id)
	if err != nil {
		return "", err
	}
	return s.d.Vault.OpenString(blob, tokenAAD(id))
}

// change runs fn on a live link inside one transaction.
func (s *Service) change(ctx context.Context, id int64, fn func(tx *sqlx.Tx, l store.Link) error) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		l, err := store.GetLink(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if l.State == "deleted" {
			return ErrDeleted
		}
		return fn(tx, l)
	})
}

// Disable makes the link serve its "disabled" stub; a disabled one stays as it is.
func (s *Service) Disable(ctx context.Context, id int64, actor string) error {
	return s.change(ctx, id, func(tx *sqlx.Tx, l store.Link) error {
		if l.State == "disabled" {
			return nil
		}
		if err := store.SetLinkState(ctx, tx, id, "disabled", s.now(), db.Time{}); err != nil {
			return err
		}
		return s.record(ctx, tx, "link.disabled", id, actor, nil)
	})
}

// Enable brings the real output back on the next fetch.
func (s *Service) Enable(ctx context.Context, id int64, actor string) error {
	return s.change(ctx, id, func(tx *sqlx.Tx, l store.Link) error {
		if l.State == "active" {
			return nil
		}
		if err := store.SetLinkState(ctx, tx, id, "active", db.Time{}, db.Time{}); err != nil {
			return err
		}
		return s.record(ctx, tx, "link.enabled", id, actor, nil)
	})
}

// RegenerateToken gives the link a new URL; the old one answers 404 at once.
func (s *Service) RegenerateToken(ctx context.Context, id int64, actor string) error {
	token := vault.NewToken()
	return s.change(ctx, id, func(tx *sqlx.Tx, _ store.Link) error {
		if err := store.SetToken(ctx, tx, id, s.d.Vault.SealString(token, tokenAAD(id)), s.d.Vault.Lookup(token)); err != nil {
			return err
		}
		return s.record(ctx, tx, "link.token_regenerated", id, actor, nil)
	})
}

// ChangeSubscription points the link at another subscription
// (subs.ErrNotFound when there is none).
func (s *Service) ChangeSubscription(ctx context.Context, id, subscriptionID int64, actor string) error {
	return s.change(ctx, id, func(tx *sqlx.Tx, l store.Link) error {
		if l.SubscriptionID.Int64 == subscriptionID {
			return nil
		}
		return s.move(ctx, tx, l, subscriptionID, actor)
	})
}

func (s *Service) move(ctx context.Context, tx *sqlx.Tx, l store.Link, to int64, actor string) error {
	from, err := store.GetSubscription(ctx, tx, l.SubscriptionID.Int64)
	if err != nil {
		return err
	}
	next, err := store.GetSubscription(ctx, tx, to)
	if errors.Is(err, store.ErrNotFound) {
		return subs.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := store.SetLinkSubscription(ctx, tx, l.ID, to); err != nil {
		return err
	}
	return s.record(ctx, tx, "link.subscription_changed", l.ID, actor, map[string]any{"from": from.Name, "to": next.Name})
}

// MoveAll moves every live link of fromSub to toSub, one event each.
// Tombstones stay where they are.
func (s *Service) MoveAll(ctx context.Context, fromSub, toSub int64, actor string) (int, error) {
	if fromSub == toSub {
		return 0, subs.ErrNotFound
	}
	moved := 0
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.GetSubscription(ctx, tx, toSub); errors.Is(err, store.ErrNotFound) {
			return subs.ErrNotFound
		} else if err != nil {
			return err
		}
		list, err := store.LinksOfSubscription(ctx, tx, fromSub)
		if err != nil {
			return err
		}
		for _, l := range list {
			if l.State == "deleted" {
				continue
			}
			if err := s.move(ctx, tx, l, toSub, actor); err != nil {
				return err
			}
			moved++
		}
		return nil
	})
	return moved, err
}

// SetExpiry sets, extends or clears (zero) the expiry. Any change re-arms the
// warning and the expired notification of 2c.
func (s *Service) SetExpiry(ctx context.Context, id int64, expires time.Time, actor string) error {
	if !expires.IsZero() && !expires.After(s.d.Now()) {
		return store.FieldErrors{"expiry": "links.err.expiry_past"}
	}
	next := db.Time{}
	if !expires.IsZero() {
		next = db.At(expires)
	}
	return s.change(ctx, id, func(tx *sqlx.Tx, l store.Link) error {
		if l.ExpiresAt.Equal(next.Time) {
			return nil
		}
		if err := store.SetLinkExpiry(ctx, tx, id, next); err != nil {
			return err
		}
		return s.record(ctx, tx, "link.expiry_changed", id, actor, map[string]any{"from": timeText(l.ExpiresAt.Time), "to": timeText(next.Time)})
	})
}

// Edit saves the name, note, language and format override; link.changed
// names the fields that changed. Errors are store.FieldErrors.
func (s *Service) Edit(ctx context.Context, id int64, e Edit, actor string) error {
	e.Name, e.Note = strings.TrimSpace(e.Name), strings.TrimSpace(e.Note)
	fe := store.FieldErrors{}
	checkFields(fe, e.Name, e.Note, e.Lang)
	return s.change(ctx, id, func(tx *sqlx.Tx, l store.Link) error {
		if err := s.nameTaken(ctx, tx, fe, e.Name, id); err != nil {
			return err
		}
		if e.Format != "" {
			sub, err := store.GetSubscription(ctx, tx, l.SubscriptionID.Int64)
			if err != nil {
				return err
			}
			if !slices.Contains(strings.Split(sub.Formats, ","), e.Format) {
				fe["format"] = "links.err.format"
			}
		}
		if len(fe) > 0 {
			return fe
		}
		var fields []string
		for _, c := range []struct {
			what string
			diff bool
		}{
			{"name", l.Name != e.Name},
			{"note", l.Note != e.Note},
			{"language", l.Language != string(e.Lang)},
			{"format", l.Format.String != e.Format},
		} {
			if c.diff {
				fields = append(fields, c.what)
			}
		}
		if len(fields) == 0 {
			return nil
		}
		format := sql.NullString{String: e.Format, Valid: e.Format != ""}
		if err := store.EditLink(ctx, tx, id, e.Name, e.Note, string(e.Lang), format); err != nil {
			if store.Unique(err, "subs_links.name") {
				return store.FieldErrors{"name": "links.err.name_taken"}
			}
			return err
		}
		return s.record(ctx, tx, "link.changed", id, actor, map[string]any{"fields": strings.Join(fields, ", ")})
	})
}

// Delete makes the link a tombstone: it leaves the lists and serves "Link
// removed" until subscriptions.tombstone has passed, then 404.
func (s *Service) Delete(ctx context.Context, id int64, actor string) error {
	return s.change(ctx, id, func(tx *sqlx.Tx, l store.Link) error {
		if err := store.SetLinkState(ctx, tx, id, "deleted", l.DisabledAt, s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, "link.deleted", id, actor, nil)
	})
}

// OfSubscription are the links of a subscription that aren't deleted, by name.
func (s *Service) OfSubscription(ctx context.Context, subID int64) ([]Link, error) {
	list, err := store.LinksOfSubscription(ctx, s.d.DB.R, subID)
	if err != nil {
		return nil, err
	}
	var out []Link
	for _, r := range list {
		if r.State != "deleted" {
			out = append(out, fromRow(r))
		}
	}
	return out, nil
}

// Filter narrows the Links list.
type Filter struct {
	Name           string // contains, any case
	SubscriptionID int64
	State          string        // live (default), active, disabled, expired, deleted, all
	ExpiringWithin time.Duration // 0: off; else active links expiring within it
}

// Row is a link on the Links list.
type Row struct {
	Link
	Subscription string // its name; "" when it has none
	Fetches24h   int
}

func (f Filter) keeps(l Link, now time.Time) bool {
	st := l.Status(now)
	switch f.State {
	case "", "live":
		if st == "deleted" {
			return false
		}
	case "all":
	default:
		if st != f.State {
			return false
		}
	}
	if f.SubscriptionID != 0 && l.SubscriptionID != f.SubscriptionID {
		return false
	}
	if f.Name != "" && !strings.Contains(strings.ToLower(l.Name), strings.ToLower(strings.TrimSpace(f.Name))) {
		return false
	}
	if f.ExpiringWithin > 0 && (st != "active" || l.Expires.IsZero() || l.Expires.Sub(now) > f.ExpiringWithin) {
		return false
	}
	return true
}

// List is the links the filter keeps, by last fetch (newest first, never
// fetched last), then by name.
func (s *Service) List(ctx context.Context, f Filter) ([]Row, error) {
	all, err := store.ListLinks(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	subList, err := store.ListSubscriptions(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, sub := range subList {
		names[sub.ID] = sub.Name
	}
	now := s.d.Now()
	counts, err := store.FetchCountsSince(ctx, s.d.DB.R, db.At(now.Add(-24*time.Hour)))
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, r := range all {
		l := fromRow(r)
		if f.keeps(l, now) {
			out = append(out, Row{Link: l, Subscription: names[l.SubscriptionID], Fetches24h: counts[l.ID]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].LastFetchAt, out[j].LastFetchAt
		if !a.Equal(b) {
			if a.IsZero() || b.IsZero() {
				return b.IsZero()
			}
			return a.After(b)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Fetches of a link, newest first, before the fetch id (0: the newest).
func (s *Service) Fetches(ctx context.Context, id, before int64, limit int) ([]store.Fetch, error) {
	return store.Fetches(ctx, s.d.DB.R, id, before, limit)
}

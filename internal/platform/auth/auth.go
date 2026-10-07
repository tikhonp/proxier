// Package auth is the domain of the one admin: the password, sessions, the
// sign-in lockout and the events they record (docs/processes/platform/sign-in.md).
// It knows nothing of HTTP.
package auth

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

const (
	IdleTimeout      = 7 * 24 * time.Hour
	AbsoluteTimeout  = 30 * 24 * time.Hour
	LastSeenEvery    = time.Minute
	NewIPWindow      = 30 * 24 * time.Hour
	AttemptRetention = 30 * 24 * time.Hour
	MinPasswordLen   = 12   // characters (runes)
	MaxPasswordLen   = 1024 // bytes
	FailureFloor     = time.Second
)

var (
	ErrAdminExists      = errors.New("auth: an admin already exists")
	ErrNoAdmin          = errors.New("auth: no admin yet")
	ErrWrongCredentials = errors.New("auth: wrong username or password")
	ErrPasswordTooShort = errors.New("auth: password shorter than 12 characters")
	ErrPasswordTooLong  = errors.New("auth: password too long")
	ErrNoSession        = errors.New("auth: no session")
)

// LockedError: the IP is locked out; nothing was checked.
type LockedError struct {
	Until time.Time
	For   time.Duration // Until minus the service's now
}

func (e *LockedError) Error() string { return "auth: too many attempts" }

// ExpiredError: the cookie names a session that ran out.
type ExpiredError struct{ Reason string } // "idle" | "absolute"

func (e *ExpiredError) Error() string { return "auth: session expired (" + e.Reason + ")" }

// Why a session ended; the values of sessions.end_reason.
const (
	EndSignedOut           = "signed_out"
	EndSignedOutEverywhere = "signed_out_everywhere"
	EndPasswordChanged     = "password_changed"
	EndPasswordReset       = "password_reset"
)

// Events are the event types auth records.
var Events = []events.Type{
	{Name: "admin.created", Module: "platform", Description: "The admin was created from the command line."},
	// Notifies only when the payload has new_ip: true (the notifier filters).
	{Name: "auth.signed_in", Module: "platform", Notify: true, Description: "Signed in from a new IP."},
	{Name: "auth.sign_in_failed", Module: "platform", Description: "A sign-in failed."},
	{Name: "auth.locked", Module: "platform", Notify: true, Description: "An IP was locked out after failed sign-ins."},
	{Name: "auth.password_changed", Module: "platform", Notify: true, Description: "The admin password was changed."},
	{Name: "auth.signed_out", Module: "platform", Description: "One session was ended."},
	{Name: "auth.signed_out_everywhere", Module: "platform", Description: "Every session was ended."},
	{Name: "admin.language_changed", Module: "platform", Description: "The admin changed the interface language."},
}

// SecuritySection is Settings → Security's lockout fields.
var SecuritySection = settings.Section{
	Name: "security", Module: "platform",
	Fields: []settings.Field{
		{Key: "security.lockout_failures", Kind: settings.Int, Default: "5", Min: 3, Max: 20},
		{Key: "security.lockout_window", Kind: settings.Duration, Default: "15m0s", Min: int64(time.Minute), Max: int64(24 * time.Hour)},
		{Key: "security.lockout_duration", Kind: settings.Duration, Default: "15m0s", Min: int64(time.Minute), Max: int64(24 * time.Hour)},
	},
}

type Admin struct {
	Username          string
	Language          i18n.Lang
	CreatedAt         db.Time
	PasswordChangedAt db.Time
}

type Session struct {
	ID                    int64
	CreatedAt, LastSeenAt db.Time
	ExpiresAt             db.Time // absolute
	CreatedIP, LastIP     string
	UserAgent             string
	// Set only for a session that ended.
	EndedAt   db.Time
	EndReason string
	// Set only on the session of the current request.
	Token string
	CSRF  string
}

// Service is the admin's account.
type Service struct {
	Params Params                                     // DefaultParams
	Now    func() time.Time                           // time.Now
	Sleep  func(ctx context.Context, d time.Duration) // pads failures to FailureFloor
	Log    *slog.Logger

	db       *db.DB
	vault    *vault.Vault
	events   *events.Catalog
	settings *settings.Store

	hashes    chan struct{} // at most two hashes at once: caps memory under a flood
	dummyOnce sync.Once
	dummy     string
}

func New(d *db.DB, v *vault.Vault, ev *events.Catalog, st *settings.Store) *Service {
	return &Service{
		Params: DefaultParams, Now: time.Now, Sleep: sleep,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		db:  d, vault: v, events: ev, settings: st,
		hashes: make(chan struct{}, 2),
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func (s *Service) now() db.Time { return db.At(s.Now()) }

// hash and verify run under the semaphore.
func (s *Service) hash(ctx context.Context, pw string) (string, error) {
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	return Hash(s.Params, pw)
}

func (s *Service) verify(ctx context.Context, phc, pw string) (bool, error) {
	if err := s.acquire(ctx); err != nil {
		return false, err
	}
	defer s.release()
	return Verify(phc, pw)
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.hashes <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) release() { <-s.hashes }

// dummyHash is what an unknown username is verified against, made once with
// the service's own parameters so both paths cost the same.
func (s *Service) dummyHash(ctx context.Context) (string, error) {
	var err error
	s.dummyOnce.Do(func() { s.dummy, err = s.hash(ctx, "proxier dummy password") })
	if err != nil {
		s.dummyOnce = sync.Once{}
	}
	return s.dummy, err
}

var adminSubject = events.Subject{Type: "admin", ID: "1"}

func (s *Service) record(ctx context.Context, tx sqlx.ExtContext, typ, actor string, subj events.Subject, payload map[string]any) error {
	_, err := s.events.Record(ctx, tx, events.Event{Time: s.now(), Type: typ, Actor: actor, Subject: subj, Payload: payload})
	return err
}

// CSRFToken is derived from the session token and never stored.
func (s *Service) CSRFToken(sessionToken string) string {
	return base64.RawURLEncoding.EncodeToString(s.vault.Lookup("csrf\x00" + sessionToken))
}

// CheckCSRF compares got with the session's token in constant time.
func (s *Service) CheckCSRF(sessionToken, got string) bool {
	if sessionToken == "" || got == "" {
		return false
	}
	return hmac.Equal([]byte(s.CSRFToken(sessionToken)), []byte(got))
}

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

const (
	// CeremonyTTL is how long a passkey prompt may stay open.
	CeremonyTTL    = 5 * time.Minute
	maxCeremonies  = 64
	MaxPasskeyName = 64 // characters
)

var (
	ErrPasskeysOff     = errors.New("auth: passkeys are not set up on this server")
	ErrCeremonyExpired = errors.New("auth: the passkey request ran out; try again")
	ErrPasskeyRejected = errors.New("auth: the passkey was not accepted")
	ErrPasskeyName     = errors.New("auth: a passkey name is 1 to 64 characters")
	ErrNoPasskey       = errors.New("auth: no such passkey")
)

// ceremony is a passkey prompt between its options and the browser's answer.
type ceremony struct {
	session int64 // adding a passkey: the session that asked; 0 when signing in
	name    string
	data    webauthn.SessionData
}

// Passkey is one passkey as Settings → Security lists it.
type Passkey struct {
	ID         int64
	Name       string
	CreatedAt  db.Time
	LastUsedAt db.Time // zero: never used
	Synced     bool    // kept in a password manager or a platform keychain
}

// EnablePasskeys makes the base URL the relying party: its host is the RP ID
// and its origin the only one a passkey answer may come from. Without it the
// passkey methods return ErrPasskeysOff.
func (s *Service) EnablePasskeys(base *url.URL) error {
	sel := protocol.AuthenticatorSelection{
		ResidentKey:        protocol.ResidentKeyRequirementRequired,
		RequireResidentKey: protocol.ResidentKeyRequired(),
		// A passkey signs in alone, so the device must check who holds it.
		UserVerification: protocol.VerificationRequired,
	}
	rp, err := webauthn.New(&webauthn.Config{
		RPID: base.Hostname(), RPDisplayName: "Proxier",
		RPOrigins:              []string{base.Scheme + "://" + base.Host},
		AuthenticatorSelection: sel,
	})
	if err != nil {
		return fmt.Errorf("auth: passkeys: %w", err)
	}
	s.rp = rp
	return nil
}

// PasskeysOn reports whether EnablePasskeys ran.
func (s *Service) PasskeysOn() bool { return s.rp != nil }

type webauthnUser struct {
	member int64
	id     []byte
	name   string
	creds  []webauthn.Credential
}

func (u webauthnUser) WebAuthnID() []byte                         { return u.id }
func (u webauthnUser) WebAuthnName() string                       { return u.name }
func (u webauthnUser) WebAuthnDisplayName() string                { return u.name }
func (u webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// webauthnUser loads the member whose column col equals key, with their
// passkeys, or ErrNoMember (for a removed member too).
func (s *Service) webauthnUser(ctx context.Context, col string, key any) (webauthnUser, error) {
	var a struct {
		Member   int64  `db:"id"`
		Username string `db:"username"`
		ID       []byte `db:"webauthn_user_id"`
	}
	err := sqlx.GetContext(ctx, s.db.R, &a, `SELECT id, username, webauthn_user_id FROM members WHERE `+col+` = ? AND removed_at IS NULL`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return webauthnUser{}, ErrNoMember
	}
	if err != nil {
		return webauthnUser{}, err
	}
	var rows []string
	if err := sqlx.SelectContext(ctx, s.db.R, &rows, `SELECT credential FROM passkeys WHERE member_id = ? ORDER BY id`, a.Member); err != nil {
		return webauthnUser{}, err
	}
	u := webauthnUser{member: a.Member, id: a.ID, name: a.Username, creds: make([]webauthn.Credential, len(rows))}
	for i, r := range rows {
		if err := json.Unmarshal([]byte(r), &u.creds[i]); err != nil {
			return webauthnUser{}, fmt.Errorf("auth: passkey %d: %w", i, err)
		}
	}
	return u, nil
}

// BeginPasskey checks the password and returns the options for
// navigator.credentials.create and the token FinishPasskey takes them back
// with. Only the session that asked can finish.
func (s *Service) BeginPasskey(ctx context.Context, sess Session, name, password string) (options []byte, token string, err error) {
	if s.rp == nil {
		return nil, "", ErrPasskeysOff
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxPasskeyName {
		return nil, "", ErrPasskeyName
	}
	if err := s.checkCurrentPassword(ctx, sess.MemberID, password); err != nil {
		return nil, "", err
	}
	user, err := s.webauthnUser(ctx, "id", sess.MemberID)
	if err != nil {
		return nil, "", err
	}
	exclude := make([]protocol.CredentialDescriptor, len(user.creds))
	for i, c := range user.creds {
		exclude[i] = c.Descriptor()
	}
	creation, data, err := s.rp.BeginRegistration(user, webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(s.rp.Config.AuthenticatorSelection))
	if err != nil {
		return nil, "", fmt.Errorf("auth: passkey options: %w", err)
	}
	if options, err = json.Marshal(creation); err != nil {
		return nil, "", err
	}
	return options, s.ceremonies.put(s.Now(), ceremony{session: sess.ID, name: name, data: *data}), nil
}

// FinishPasskey checks the browser's answer and stores the passkey.
func (s *Service) FinishPasskey(ctx context.Context, sess Session, token string, body []byte) error {
	if s.rp == nil {
		return ErrPasskeysOff
	}
	c, _, ok := s.ceremonies.take(token, s.Now())
	if !ok || c.session == 0 || c.session != sess.ID {
		return ErrCeremonyExpired
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return s.rejected("add", err)
	}
	user, err := s.webauthnUser(ctx, "id", sess.MemberID)
	if err != nil {
		return err
	}
	cred, err := s.rp.CreateCredential(user, c.data, parsed)
	if err != nil {
		return s.rejected("add", err)
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var n int
		if err := sqlx.GetContext(ctx, tx, &n, `SELECT COUNT(*) FROM passkeys WHERE credential_id = ?`, cred.ID); err != nil {
			return err
		}
		if n > 0 {
			return s.rejected("add", errors.New("already added"))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO passkeys (member_id, credential_id, name, credential, created_at) VALUES (?, ?, ?, ?, ?)`,
			sess.MemberID, cred.ID, c.name, string(raw), s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, "auth.passkey_added", events.MemberActor(sess.MemberID), MemberSubject(sess.MemberID), map[string]any{"name": c.name})
	})
}

// BeginPasskeySignIn returns the options for navigator.credentials.get and
// the token FinishPasskeySignIn takes them back with. It names no passkey or
// member: the browser offers the ones it holds for this site, and the answer's
// user handle says whose it is.
func (s *Service) BeginPasskeySignIn() (options []byte, token string, err error) {
	if s.rp == nil {
		return nil, "", ErrPasskeysOff
	}
	assertion, data, err := s.rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", fmt.Errorf("auth: passkey options: %w", err)
	}
	if options, err = json.Marshal(assertion); err != nil {
		return nil, "", err
	}
	return options, s.ceremonies.put(s.Now(), ceremony{data: *data}), nil
}

// FinishPasskeySignIn checks the lockout, then the passkey, and creates a
// session. It returns *LockedError, ErrCeremonyExpired, or ErrPasskeyRejected
// (unknown or removed passkey, bad signature, no user verification, a
// signature counter that went back), which is a failed attempt.
func (s *Service) FinishPasskeySignIn(ctx context.Context, token string, body []byte, ip, userAgent string) (SignInResult, error) {
	if s.rp == nil {
		return SignInResult{}, ErrPasskeysOff
	}
	start := s.Now()
	rules, err := s.gate(ctx, ip, start)
	if err != nil {
		return SignInResult{}, err
	}
	c, _, ok := s.ceremonies.take(token, start)
	if !ok || c.session != 0 {
		return SignInResult{}, ErrCeremonyExpired
	}
	var user webauthnUser // whose passkey it is, once the user handle names a member
	reject := func(cause error) (SignInResult, error) {
		s.Log.Info("passkey sign-in refused", "ip", ip, "error", cause)
		if err := s.fail(ctx, rules, start, ip, user.member, map[string]any{"stage": "passkey"}); err != nil {
			return SignInResult{}, err
		}
		return SignInResult{}, ErrPasskeyRejected
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return reject(err)
	}
	_, cred, err := s.rp.ValidatePasskeyLogin(func(_, handle []byte) (webauthn.User, error) {
		u, err := s.webauthnUser(ctx, "webauthn_user_id", handle)
		if err == nil {
			user = u
		}
		return u, err
	}, c.data, parsed)
	if err != nil {
		return reject(err)
	}
	if cred.Authenticator.CloneWarning {
		return reject(errors.New("the signature counter went back: the passkey may be cloned"))
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return SignInResult{}, err
	}
	errRemoved := errors.New("the passkey was removed meanwhile")
	var res SignInResult
	err = s.db.Write(ctx, func(tx *sqlx.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE passkeys SET credential = ?, last_used_at = ? WHERE credential_id = ? AND member_id = ?`, string(raw), s.now(), cred.ID, user.member)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return errRemoved
		}
		res, err = s.createSessionTx(ctx, tx, user.member, ip, userAgent, MethodPasskey)
		return err
	})
	if errors.Is(err, errRemoved) {
		return reject(err)
	}
	return res, err
}

func (s *Service) rejected(what string, cause error) error {
	s.Log.Info("passkey refused", "ceremony", what, "error", cause)
	return ErrPasskeyRejected
}

// Passkeys lists a member's passkeys, oldest first.
func (s *Service) Passkeys(ctx context.Context, memberID int64) ([]Passkey, error) {
	var rows []struct {
		ID         int64    `db:"id"`
		Name       string   `db:"name"`
		Credential string   `db:"credential"`
		CreatedAt  db.Time  `db:"created_at"`
		LastUsedAt *db.Time `db:"last_used_at"`
	}
	if err := sqlx.SelectContext(ctx, s.db.R, &rows, `SELECT id, name, credential, created_at, last_used_at FROM passkeys WHERE member_id = ? ORDER BY id`, memberID); err != nil {
		return nil, err
	}
	out := make([]Passkey, len(rows))
	for i, r := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(r.Credential), &c); err != nil {
			return nil, fmt.Errorf("auth: passkey %d: %w", r.ID, err)
		}
		out[i] = Passkey{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, Synced: c.Flags.BackupState}
		if r.LastUsedAt != nil {
			out[i].LastUsedAt = *r.LastUsedAt
		}
	}
	return out, nil
}

// HasPasskeys reports whether the sign-in page offers a passkey: whether any
// member has one, since the page doesn't know who is signing in.
func (s *Service) HasPasskeys(ctx context.Context) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, s.db.R, &n, `SELECT COUNT(*) FROM passkeys`)
	return n > 0, err
}

// RemovePasskey removes one of the member's passkeys; it needs their current
// password.
func (s *Service) RemovePasskey(ctx context.Context, memberID, id int64, password string) error {
	if err := s.checkCurrentPassword(ctx, memberID, password); err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var name string
		err := sqlx.GetContext(ctx, tx, &name, `SELECT name FROM passkeys WHERE id = ? AND member_id = ?`, id, memberID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoPasskey
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ?`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "auth.passkey_removed", events.MemberActor(memberID), MemberSubject(memberID), map[string]any{"name": name})
	})
}

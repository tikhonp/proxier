package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// RFC 6238 as every authenticator app reads an otpauth:// URI without
// parameters: HMAC-SHA1, 6 digits, 30 s steps.
const (
	totpPeriod    = 30 * time.Second
	totpDigits    = 6
	totpSecretLen = 20
	// How long the secret shown by Set up stays good for Turn on.
	TOTPSetupTTL = 15 * time.Minute
)

var (
	ErrTOTPOn       = errors.New("auth: the authenticator app is already on")
	ErrTOTPOff      = errors.New("auth: the authenticator app is off")
	ErrWrongCode    = errors.New("auth: wrong code")
	ErrSetupExpired = errors.New("auth: the authenticator app setup ran out; start again")
	ErrSetupInvalid = errors.New("auth: the authenticator app setup is not of this session")
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// hotp is RFC 4226's code of counter, digits long.
func hotp(secret []byte, counter uint64, digits int) string {
	m := hmac.New(sha1.New, secret)
	_ = binary.Write(m, binary.BigEndian, counter)
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

func totpStep(t time.Time) int64 { return t.Unix() / int64(totpPeriod/time.Second) }

// matchTOTP finds the step of code among now's step and its neighbours (clock
// drift) that is newer than last, the step of the code used before.
func matchTOTP(secret []byte, code string, now time.Time, last int64) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := totpStep(now)
	for step := cur - 1; step <= cur+1; step++ {
		if step <= last || step < 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hotp(secret, uint64(step), totpDigits)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// TOTPSetup is what Set up shows: the secret for the QR code and for typing
// in, and Token, the sealed secret the form posts back with the first code.
type TOTPSetup struct {
	Secret string // base32
	URI    string // otpauth://
	Token  string
}

// totpAAD names where a member's secret lives. Member 1's secret was sealed
// as the one admin's before members existed and keeps that AAD: migrations
// are SQL without the master key, so they can't re-seal it.
func totpAAD(member int64) string {
	if member == 1 {
		return "admin:totp"
	}
	return "member:" + strconv.FormatInt(member, 10) + ":totp"
}

func setupAAD(sessionID int64) string { return "totp-setup:" + strconv.FormatInt(sessionID, 10) }

// BeginTOTP checks the password and makes a new secret. Nothing is stored
// until EnableTOTP: the secret travels sealed to this session.
func (s *Service) BeginTOTP(ctx context.Context, sess Session, password string) (TOTPSetup, error) {
	if on, err := s.TOTPEnabled(ctx, sess.MemberID); err != nil {
		return TOTPSetup{}, err
	} else if on {
		return TOTPSetup{}, ErrTOTPOn
	}
	if err := s.checkCurrentPassword(ctx, sess.MemberID, password); err != nil {
		return TOTPSetup{}, err
	}
	m, err := s.Member(ctx, sess.MemberID)
	if err != nil {
		return TOTPSetup{}, err
	}
	secret := make([]byte, totpSecretLen)
	_, _ = rand.Read(secret)
	plain := binary.BigEndian.AppendUint64(append([]byte(nil), secret...), uint64(s.Now().Unix()))
	return totpSetup(secret, m.Username, base64.RawURLEncoding.EncodeToString(s.vault.Seal(plain, setupAAD(sess.ID)))), nil
}

func totpSetup(secret []byte, username, token string) TOTPSetup {
	q := url.Values{"secret": {b32.EncodeToString(secret)}, "issuer": {"Proxier"}}
	return TOTPSetup{
		Secret: b32.EncodeToString(secret),
		URI:    "otpauth://totp/" + url.PathEscape("Proxier:"+username) + "?" + q.Encode(),
		Token:  token,
	}
}

// openSetup returns the secret of a setup token of this session.
func (s *Service) openSetup(sessionID int64, token string) ([]byte, error) {
	blob, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrSetupInvalid
	}
	plain, err := s.vault.Open(blob, setupAAD(sessionID))
	if err != nil || len(plain) != totpSecretLen+8 {
		return nil, ErrSetupInvalid
	}
	made := time.Unix(int64(binary.BigEndian.Uint64(plain[totpSecretLen:])), 0)
	if s.Now().Sub(made) > TOTPSetupTTL {
		return nil, ErrSetupExpired
	}
	return plain[:totpSecretLen], nil
}

// ResumeTOTP shows a setup again after a wrong code.
func (s *Service) ResumeTOTP(ctx context.Context, sess Session, token string) (TOTPSetup, error) {
	secret, err := s.openSetup(sess.ID, token)
	if err != nil {
		return TOTPSetup{}, err
	}
	m, err := s.Member(ctx, sess.MemberID)
	if err != nil {
		return TOTPSetup{}, err
	}
	return totpSetup(secret, m.Username, token), nil
}

// EnableTOTP turns the member's authenticator app on once they type a code
// from the secret BeginTOTP made for this session.
func (s *Service) EnableTOTP(ctx context.Context, sess Session, token, code string) error {
	secret, err := s.openSetup(sess.ID, token)
	if err != nil {
		return err
	}
	step, ok := matchTOTP(secret, code, s.Now(), 0)
	if !ok {
		return ErrWrongCode
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		var n int
		if err := sqlx.GetContext(ctx, tx, &n, `SELECT COUNT(*) FROM totp WHERE member_id = ?`, sess.MemberID); err != nil {
			return err
		}
		if n > 0 {
			return ErrTOTPOn
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO totp (member_id, secret, created_at, last_step) VALUES (?, ?, ?, ?)`,
			sess.MemberID, s.vault.Seal(secret, totpAAD(sess.MemberID)), s.now(), step); err != nil {
			return err
		}
		return s.record(ctx, tx, "auth.totp_enabled", events.MemberActor(sess.MemberID), MemberSubject(sess.MemberID), nil)
	})
}

// DisableTOTP needs the member's current password.
func (s *Service) DisableTOTP(ctx context.Context, memberID int64, password string) error {
	if err := s.checkCurrentPassword(ctx, memberID, password); err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM totp WHERE member_id = ?`, memberID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrTOTPOff
		}
		return s.record(ctx, tx, "auth.totp_disabled", events.MemberActor(memberID), MemberSubject(memberID), nil)
	})
}

// TOTPEnabled reports whether the member's password sign-in asks for a code.
func (s *Service) TOTPEnabled(ctx context.Context, memberID int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, s.db.R, &n, `SELECT COUNT(*) FROM totp WHERE member_id = ?`, memberID)
	return n > 0, err
}

// totpSecret reads the member's secret and last used step inside tx. ok is
// false when the app is off.
func (s *Service) totpSecret(ctx context.Context, tx sqlx.QueryerContext, member int64) (secret []byte, last int64, ok bool, err error) {
	var r struct {
		Secret []byte `db:"secret"`
		Last   int64  `db:"last_step"`
	}
	err = sqlx.GetContext(ctx, tx, &r, `SELECT secret, last_step FROM totp WHERE member_id = ?`, member)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	secret, err = s.vault.Open(r.Secret, totpAAD(member))
	if err != nil {
		return nil, 0, false, fmt.Errorf("auth: open the authenticator app secret: %w", err)
	}
	return secret, r.Last, true, nil
}

// checkCurrentPassword guards the changes a stolen session must not be able
// to make: adding or removing a way to sign in.
func (s *Service) checkCurrentPassword(ctx context.Context, member int64, password string) error {
	phc, err := s.passwordHash(ctx, member)
	if err != nil {
		return err
	}
	ok, err := s.verify(ctx, phc, password)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWrongCredentials
	}
	return nil
}

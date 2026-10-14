package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// Method is how a session was signed in: auth.signed_in's method.
type Method string

const (
	MethodPassword     Method = "password"
	MethodPasswordCode Method = "password+code"
	MethodPasskey      Method = "passkey"
)

const (
	// PendingTTL is how long the code step waits after a right password.
	PendingTTL   = 5 * time.Minute
	MaxCodeTries = 5
	maxPending   = 64
)

// SignInResult is a new session, or a sign-in waiting for its code.
type SignInResult struct {
	Session Session // with Token and CSRF
	NewIP   bool
	// Pending is set instead of a session when the password was right and the
	// authenticator app is on: SignInCode finishes the sign-in with it.
	Pending string
}

// pendingSignIn is a right password waiting for its code.
type pendingSignIn struct {
	member int64
	ip     string
	tries  int
}

type lockoutRules struct {
	failures         int
	window, duration time.Duration
}

func (s *Service) rules(ctx context.Context) (lockoutRules, error) {
	n, err := s.settings.GetInt(ctx, "security.lockout_failures")
	if err != nil {
		return lockoutRules{}, err
	}
	w, err := s.settings.GetDuration(ctx, "security.lockout_window")
	if err != nil {
		return lockoutRules{}, err
	}
	d, err := s.settings.GetDuration(ctx, "security.lockout_duration")
	if err != nil {
		return lockoutRules{}, err
	}
	return lockoutRules{int(n), w, d}, nil
}

// lockedUntil derives the lockout from the attempt history: the IP is locked
// when its last N failures since its last success span no more than the
// window, until the latest of them plus the duration. Zero time: not locked.
func (r lockoutRules) lockedUntil(ctx context.Context, q sqlx.QueryerContext, ip string, now time.Time) (time.Time, error) {
	var times []db.Time
	err := sqlx.SelectContext(ctx, q, &times, `
		SELECT time FROM sign_in_attempts
		WHERE ip = ? AND success = 0
		  AND time > COALESCE((SELECT MAX(time) FROM sign_in_attempts WHERE ip = ? AND success = 1), '')
		ORDER BY time DESC LIMIT ?`, ip, ip, r.failures)
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: lockout: %w", err)
	}
	if len(times) < r.failures {
		return time.Time{}, nil
	}
	last, first := times[0].Time, times[len(times)-1].Time
	if last.Sub(first) > r.window {
		return time.Time{}, nil
	}
	if until := last.Add(r.duration); until.After(now) {
		return until, nil
	}
	return time.Time{}, nil
}

// SignIn checks the lockout, then the credentials. It returns *LockedError
// or ErrWrongCredentials, for an unknown username too. Failures are padded to
// FailureFloor. With the authenticator app on, a right password
// returns Pending and records no success: the code may still be wrong, and a
// success would reset the IP's failure count.
func (s *Service) SignIn(ctx context.Context, username, password, ip, userAgent string) (SignInResult, error) {
	start := s.Now()
	rules, err := s.gate(ctx, ip, start)
	if err != nil {
		return SignInResult{}, err
	}

	member, phc, err := s.credentials(ctx, username)
	if err != nil {
		return SignInResult{}, err
	}
	known := member != 0
	if !known {
		if phc, err = s.dummyHash(ctx); err != nil {
			return SignInResult{}, err
		}
	}
	// The hash is verified even for a wrong username: same cost either way.
	ok, err := s.verify(ctx, phc, password)
	if err != nil {
		return SignInResult{}, err
	}
	if !ok || !known {
		if len(username) > 64 {
			username = username[:64]
		}
		if err := s.fail(ctx, rules, start, ip, member, map[string]any{"username_tried": username}); err != nil {
			return SignInResult{}, err
		}
		return SignInResult{}, ErrWrongCredentials
	}
	if on, err := s.TOTPEnabled(ctx, member); err != nil {
		return SignInResult{}, err
	} else if on {
		return SignInResult{Pending: s.pending.put(s.Now(), pendingSignIn{member: member, ip: ip})}, nil
	}
	return s.createSession(ctx, member, ip, userAgent, MethodPassword)
}

// SignInCode finishes a pending sign-in with a code from the authenticator
// app. It returns *LockedError, ErrSignInExpired (no such pending sign-in, from
// another IP, out of tries or the app turned off meanwhile) or ErrWrongCode.
// A wrong code is a failed attempt like a wrong password.
func (s *Service) SignInCode(ctx context.Context, pending, code, ip, userAgent string) (SignInResult, error) {
	start := s.Now()
	rules, err := s.gate(ctx, ip, start)
	if err != nil {
		return SignInResult{}, err
	}
	p, expires, ok := s.pending.take(pending, start)
	if !ok || p.ip != ip {
		return SignInResult{}, ErrSignInExpired
	}
	var res SignInResult
	err = s.db.Write(ctx, func(tx *sqlx.Tx) error {
		secret, last, on, err := s.totpSecret(ctx, tx, p.member)
		if err != nil {
			return err
		}
		if !on {
			return ErrSignInExpired
		}
		step, ok := matchTOTP(secret, code, start, last)
		if !ok {
			return ErrWrongCode
		}
		if _, err := tx.ExecContext(ctx, `UPDATE totp SET last_step = ? WHERE member_id = ?`, step, p.member); err != nil {
			return err
		}
		res, err = s.createSessionTx(ctx, tx, p.member, ip, userAgent, MethodPasswordCode)
		return err
	})
	if !errors.Is(err, ErrWrongCode) {
		return res, err
	}
	if p.tries+1 < MaxCodeTries {
		s.pending.putAt(pending, pendingSignIn{member: p.member, ip: ip, tries: p.tries + 1}, expires, start)
	}
	if err := s.fail(ctx, rules, start, ip, p.member, map[string]any{"stage": "code"}); err != nil {
		return SignInResult{}, err
	}
	return SignInResult{}, ErrWrongCode
}

// fail records a failed attempt from ip, naming member (0: none), and pads
// the request to FailureFloor.
func (s *Service) fail(ctx context.Context, rules lockoutRules, start time.Time, ip string, member int64, payload map[string]any) error {
	payload["ip"] = ip
	if err := s.recordFailure(ctx, rules, ip, member, payload); err != nil {
		return err
	}
	if rest := FailureFloor - s.Now().Sub(start); rest > 0 {
		s.Sleep(ctx, rest)
	}
	return nil
}

// gate reads the lockout rules and refuses a locked IP with *LockedError
// before anything is checked.
func (s *Service) gate(ctx context.Context, ip string, start time.Time) (lockoutRules, error) {
	rules, err := s.rules(ctx)
	if err != nil {
		return rules, err
	}
	if until, err := rules.lockedUntil(ctx, s.db.R, ip, start); err != nil {
		return rules, err
	} else if !until.IsZero() {
		return rules, &LockedError{Until: until, For: until.Sub(start)}
	}
	return rules, nil
}

// credentials finds a member who can sign in by username, ignoring case.
// member is 0 for an unknown username, a removed member and one who hasn't
// finished setup: none of them has a password that signs in.
func (s *Service) credentials(ctx context.Context, username string) (member int64, phc string, err error) {
	var r struct {
		ID   int64  `db:"id"`
		Hash string `db:"password_hash"`
	}
	err = sqlx.GetContext(ctx, s.db.R, &r, `SELECT id, password_hash FROM members WHERE username = ? AND removed_at IS NULL AND password_hash IS NOT NULL`, username)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return r.ID, r.Hash, err
}

// passwordHash is a member's password hash, or ErrNoMember for a member
// without one that signs in.
func (s *Service) passwordHash(ctx context.Context, member int64) (string, error) {
	var phc string
	err := sqlx.GetContext(ctx, s.db.R, &phc, `SELECT password_hash FROM members WHERE id = ? AND removed_at IS NULL AND password_hash IS NOT NULL`, member)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoMember
	}
	return phc, err
}

func (s *Service) recordFailure(ctx context.Context, rules lockoutRules, ip string, member int64, payload map[string]any) error {
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO sign_in_attempts (ip, time, success, member_id) VALUES (?, ?, 0, ?)`, ip, now, nullID(member)); err != nil {
			return err
		}
		if err := s.record(ctx, tx, "auth.sign_in_failed", "system", events.Subject{}, payload); err != nil {
			return err
		}
		// Attempts during a lockout are not recorded, so this can only be the
		// failure that starts one: one auth.locked per lockout.
		until, err := rules.lockedUntil(ctx, tx, ip, now.Time)
		if err != nil {
			return err
		}
		if !until.IsZero() {
			return s.record(ctx, tx, "auth.locked", "system", events.Subject{}, map[string]any{"ip": ip, "failures": rules.failures, "minutes": int(rules.duration.Minutes())})
		}
		return nil
	})
}

func nullID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func (s *Service) createSession(ctx context.Context, member int64, ip, userAgent string, m Method) (res SignInResult, err error) {
	err = s.db.Write(ctx, func(tx *sqlx.Tx) error {
		res, err = s.createSessionTx(ctx, tx, member, ip, userAgent, m)
		return err
	})
	return res, err
}

func (s *Service) createSessionTx(ctx context.Context, tx *sqlx.Tx, member int64, ip, userAgent string, m Method) (SignInResult, error) {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	token := vault.NewToken()
	var res SignInResult
	now := s.now()
	var recent int
	if err := sqlx.GetContext(ctx, tx, &recent, `SELECT COUNT(*) FROM sign_in_attempts WHERE ip = ? AND member_id = ? AND success = 1 AND time > ?`,
		ip, member, db.At(now.Add(-NewIPWindow))); err != nil {
		return res, err
	}
	res.NewIP = recent == 0
	if _, err := tx.ExecContext(ctx, `DELETE FROM sign_in_attempts WHERE time < ?`, db.At(now.Add(-AttemptRetention))); err != nil {
		return res, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sign_in_attempts (ip, time, success, member_id) VALUES (?, ?, 1, ?)`, ip, now, member); err != nil {
		return res, err
	}
	r, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, member_id, created_at, last_seen_at, expires_at, created_ip, last_ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.vault.Lookup(token), member, now, now, db.At(now.Add(AbsoluteTimeout)), ip, ip, userAgent)
	if err != nil {
		return res, err
	}
	id, _ := r.LastInsertId()
	res.Session = Session{ID: id, MemberID: member, CreatedAt: now, LastSeenAt: now, ExpiresAt: db.At(now.Add(AbsoluteTimeout)),
		CreatedIP: ip, LastIP: ip, UserAgent: userAgent, Token: token, CSRF: s.CSRFToken(token)}
	return res, s.record(ctx, tx, "auth.signed_in", events.MemberActor(member), MemberSubject(member),
		map[string]any{"ip": ip, "user_agent": userAgent, "new_ip": res.NewIP, "method": string(m)})
}

package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// SignInResult is a new session.
type SignInResult struct {
	Session Session // with Token and CSRF
	NewIP   bool
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

// SignIn checks the lockout, then the credentials. It returns *LockedError,
// ErrWrongCredentials or ErrNoAdmin (shown as wrong credentials). Failures
// are padded to FailureFloor.
func (s *Service) SignIn(ctx context.Context, username, password, ip, userAgent string) (SignInResult, error) {
	start := s.Now()
	rules, err := s.rules(ctx)
	if err != nil {
		return SignInResult{}, err
	}
	if until, err := rules.lockedUntil(ctx, s.db.R, ip, start); err != nil {
		return SignInResult{}, err
	} else if !until.IsZero() {
		return SignInResult{}, &LockedError{Until: until, For: until.Sub(start)}
	}

	phc, name, err := s.credentials(ctx)
	if err != nil {
		return SignInResult{}, err
	}
	known := phc != ""
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
	ok = ok && known && subtle.ConstantTimeCompare([]byte(username), []byte(name)) == 1

	if !ok {
		if err := s.recordFailure(ctx, rules, ip, username); err != nil {
			return SignInResult{}, err
		}
		if rest := FailureFloor - s.Now().Sub(start); rest > 0 {
			s.Sleep(ctx, rest)
		}
		if !known {
			return SignInResult{}, ErrNoAdmin
		}
		return SignInResult{}, ErrWrongCredentials
	}
	return s.createSession(ctx, ip, userAgent)
}

func (s *Service) credentials(ctx context.Context) (phc, username string, err error) {
	var r struct {
		Username string `db:"username"`
		Hash     string `db:"password_hash"`
	}
	err = sqlx.GetContext(ctx, s.db.R, &r, `SELECT username, password_hash FROM admin WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return r.Hash, r.Username, err
}

func (s *Service) recordFailure(ctx context.Context, rules lockoutRules, ip, username string) error {
	if len(username) > 64 {
		username = username[:64]
	}
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO sign_in_attempts (ip, time, success) VALUES (?, ?, 0)`, ip, now); err != nil {
			return err
		}
		if err := s.record(ctx, tx, "auth.sign_in_failed", "system", events.Subject{}, map[string]any{"ip": ip, "username_tried": username}); err != nil {
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

func (s *Service) createSession(ctx context.Context, ip, userAgent string) (SignInResult, error) {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	token := vault.NewToken()
	var res SignInResult
	err := s.db.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		var recent int
		if err := sqlx.GetContext(ctx, tx, &recent, `SELECT COUNT(*) FROM sign_in_attempts WHERE ip = ? AND success = 1 AND time > ?`,
			ip, db.At(now.Add(-NewIPWindow))); err != nil {
			return err
		}
		res.NewIP = recent == 0
		if _, err := tx.ExecContext(ctx, `DELETE FROM sign_in_attempts WHERE time < ?`, db.At(now.Add(-AttemptRetention))); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sign_in_attempts (ip, time, success) VALUES (?, ?, 1)`, ip, now); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (token_hash, admin_id, created_at, last_seen_at, expires_at, created_ip, last_ip, user_agent)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?)`,
			s.vault.Lookup(token), now, now, db.At(now.Add(AbsoluteTimeout)), ip, ip, userAgent)
		if err != nil {
			return err
		}
		id, _ := r.LastInsertId()
		res.Session = Session{ID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: db.At(now.Add(AbsoluteTimeout)),
			CreatedIP: ip, LastIP: ip, UserAgent: userAgent, Token: token, CSRF: s.CSRFToken(token)}
		return s.record(ctx, tx, "auth.signed_in", "admin", adminSubject,
			map[string]any{"ip": ip, "user_agent": userAgent, "new_ip": res.NewIP})
	})
	return res, err
}

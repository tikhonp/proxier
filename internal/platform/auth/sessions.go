package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

type sessionRow struct {
	ID         int64    `db:"id"`
	CreatedAt  db.Time  `db:"created_at"`
	LastSeenAt db.Time  `db:"last_seen_at"`
	ExpiresAt  db.Time  `db:"expires_at"`
	CreatedIP  string   `db:"created_ip"`
	LastIP     string   `db:"last_ip"`
	UserAgent  string   `db:"user_agent"`
	EndedAt    *db.Time `db:"ended_at"`
	EndReason  *string  `db:"end_reason"`
}

const sessionCols = `id, created_at, last_seen_at, expires_at, created_ip, last_ip, user_agent, ended_at, end_reason`

func (r sessionRow) session() Session {
	s := Session{ID: r.ID, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.ExpiresAt,
		CreatedIP: r.CreatedIP, LastIP: r.LastIP, UserAgent: r.UserAgent}
	if r.EndedAt != nil {
		s.EndedAt = *r.EndedAt
	}
	if r.EndReason != nil {
		s.EndReason = *r.EndReason
	}
	return s
}

// expiry says why a session has run out, "" when it is live.
func expiry(r sessionRow, now time.Time) string {
	switch {
	case !now.Before(r.ExpiresAt.Time):
		return "absolute"
	case !now.Before(r.LastSeenAt.Add(IdleTimeout)):
		return "idle"
	}
	return ""
}

// Authenticate finds the open session for a cookie token and refreshes its
// last-seen time (at most once a minute). ErrNoSession or *ExpiredError.
func (s *Service) Authenticate(ctx context.Context, token, ip string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	var r sessionRow
	err := sqlx.GetContext(ctx, s.db.R, &r, `SELECT `+sessionCols+` FROM sessions WHERE token_hash = ? AND ended_at IS NULL`, s.vault.Lookup(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	now := s.Now()
	if why := expiry(r, now); why != "" {
		return Session{}, &ExpiredError{Reason: why}
	}
	if now.Sub(r.LastSeenAt.Time) >= LastSeenEvery {
		// Bookkeeping only: a failure must not fail the request.
		err := s.db.Write(ctx, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, last_ip = ? WHERE id = ?`, db.At(now), ip, r.ID)
			return err
		})
		if err != nil {
			s.Log.Warn("session last seen", "error", err)
		} else {
			r.LastSeenAt, r.LastIP = db.At(now), ip
		}
	}
	out := r.session()
	out.Token, out.CSRF = token, s.CSRFToken(token)
	return out, nil
}

// Sessions lists the open, unexpired sessions, newest first.
func (s *Service) Sessions(ctx context.Context) ([]Session, error) {
	var rows []sessionRow
	if err := sqlx.SelectContext(ctx, s.db.R, &rows, `SELECT `+sessionCols+` FROM sessions WHERE ended_at IS NULL ORDER BY created_at DESC, id DESC`); err != nil {
		return nil, err
	}
	now := s.Now()
	var out []Session
	for _, r := range rows {
		if expiry(r, now) == "" {
			out = append(out, r.session())
		}
	}
	return out, nil
}

// RecentEnded lists sessions that were ended on purpose, newest end first.
func (s *Service) RecentEnded(ctx context.Context, limit int) ([]Session, error) {
	var rows []sessionRow
	if err := sqlx.SelectContext(ctx, s.db.R, &rows, `SELECT `+sessionCols+` FROM sessions WHERE ended_at IS NOT NULL ORDER BY ended_at DESC, id DESC LIMIT ?`, limit); err != nil {
		return nil, err
	}
	out := make([]Session, len(rows))
	for i, r := range rows {
		out[i] = r.session()
	}
	return out, nil
}

// SignOut ends one session; by is "self" or "settings". Ending a session that
// is already over records nothing.
func (s *Service) SignOut(ctx context.Context, sessionID int64, by string) error {
	return s.db.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`, now, EndSignedOut, sessionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return s.record(ctx, tx, "auth.signed_out", "admin", adminSubject, map[string]any{"session": sessionID, "by": by})
	})
}

// endLive ends every open session but keep (0 keeps none) and returns how many.
func (s *Service) endLive(ctx context.Context, tx *sqlx.Tx, reason string, keep int64) (int, error) {
	now := s.Now()
	res, err := tx.ExecContext(ctx, `
		UPDATE sessions SET ended_at = ?, end_reason = ?
		WHERE ended_at IS NULL AND id != ? AND expires_at > ? AND last_seen_at > ?`,
		db.At(now), reason, keep, db.At(now), db.At(now.Add(-IdleTimeout)))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SignOutEverywhere ends every session, this one too.
func (s *Service) SignOutEverywhere(ctx context.Context) (int, error) {
	var n int
	err := s.db.Write(ctx, func(tx *sqlx.Tx) (err error) {
		if n, err = s.endLive(ctx, tx, EndSignedOutEverywhere, 0); err != nil || n == 0 {
			return err
		}
		return s.record(ctx, tx, "auth.signed_out_everywhere", "admin", adminSubject, map[string]any{"sessions": n})
	})
	return n, err
}

package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// AgentSession is a script's draft handed to a coding agent
// (rscripts_agent_sessions). Only the keyed hash of its token is stored.
type AgentSession struct {
	ID          int64   `db:"id"`
	ScriptID    int64   `db:"script_id"`
	TokenLookup []byte  `db:"token_lookup"`
	Problem     string  `db:"problem"`
	Context     string  `db:"context"` // JSON, agent.Context
	Agent       string  `db:"agent"`
	OpenedAt    db.Time `db:"opened_at"`
	ExpiresAt   db.Time `db:"expires_at"`
	ClosedAt    db.Time `db:"closed_at"`
	CloseReason string  `db:"close_reason"`
	Saves       int     `db:"saves"`
	Requests    int     `db:"requests"`
	MinuteStart db.Time `db:"minute_start"`
	MinuteCount int     `db:"minute_count"`
}

// Open reports whether the session has not been closed.
func (s AgentSession) Open() bool { return s.ClosedAt.IsZero() }

const agentSelect = `SELECT id, script_id, token_lookup, problem, context, agent, opened_at, expires_at, closed_at,
	coalesce(close_reason, '') AS close_reason, saves, requests, minute_start, minute_count FROM rscripts_agent_sessions`

// InsertAgentSession adds an open session. The partial unique index refuses a
// second open one for the script: close the first in the same transaction.
func InsertAgentSession(ctx context.Context, x sqlx.ExtContext, s AgentSession) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO rscripts_agent_sessions (script_id, token_lookup, problem, context, agent, opened_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, s.ScriptID, s.TokenLookup, s.Problem, s.Context, s.Agent, s.OpenedAt, s.ExpiresAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AgentSessionByID returns one session.
func AgentSessionByID(ctx context.Context, q sqlx.QueryerContext, id int64) (AgentSession, error) {
	var s AgentSession
	return s, notFound(sqlx.GetContext(ctx, q, &s, agentSelect+` WHERE id = ?`, id))
}

// AgentSessionByToken finds a session by vault.Lookup(token), closed ones too.
func AgentSessionByToken(ctx context.Context, q sqlx.QueryerContext, lookup []byte) (AgentSession, error) {
	var s AgentSession
	return s, notFound(sqlx.GetContext(ctx, q, &s, agentSelect+` WHERE token_lookup = ?`, lookup))
}

// OpenAgentSession returns the script's open session.
func OpenAgentSession(ctx context.Context, q sqlx.QueryerContext, scriptID int64) (AgentSession, error) {
	var s AgentSession
	return s, notFound(sqlx.GetContext(ctx, q, &s, agentSelect+` WHERE script_id = ? AND closed_at IS NULL`, scriptID))
}

// ExpiredAgentSessions lists the open sessions whose time has passed.
func ExpiredAgentSessions(ctx context.Context, q sqlx.QueryerContext, now db.Time) ([]AgentSession, error) {
	var out []AgentSession
	err := sqlx.SelectContext(ctx, q, &out, agentSelect+` WHERE closed_at IS NULL AND expires_at <= ? ORDER BY id`, now)
	return out, err
}

// CloseAgentSession ends a session. It reports whether it was still open.
func CloseAgentSession(ctx context.Context, x sqlx.ExtContext, id int64, reason string, at db.Time) (bool, error) {
	res, err := x.ExecContext(ctx, `UPDATE rscripts_agent_sessions SET closed_at = ?, close_reason = ? WHERE id = ? AND closed_at IS NULL`, at, reason, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetAgentRate stores a session's request counters.
func SetAgentRate(ctx context.Context, x sqlx.ExtContext, id int64, requests int, minuteStart db.Time, minuteCount int) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_agent_sessions SET requests = ?, minute_start = ?, minute_count = ? WHERE id = ?`,
		requests, minuteStart, minuteCount, id)
	return err
}

// AddAgentSave counts a save of the session's.
func AddAgentSave(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_agent_sessions SET saves = saves + 1 WHERE id = ?`, id)
	return err
}

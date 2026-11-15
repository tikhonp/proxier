package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// AIResult is one attempt. A missing score is distinct from zero.
type AIResult struct {
	ID          int64    `db:"id"`
	ServerID    int64    `db:"server_id"`
	EndpointKey string   `db:"endpoint_key"`
	Generation  string   `db:"generation"`
	Service     string   `db:"service"`
	AttemptedAt db.Time  `db:"attempted_at"`
	ObservedAt  db.Time  `db:"observed_at"`
	IP          string   `db:"ip"`
	Family      int      `db:"family"`
	Reachable   bool     `db:"reachable"`
	HTTPStatus  int      `db:"http_status"`
	Error       string   `db:"error"`
	LookupAt    db.Time  `db:"lookup_at"`
	RawScore    *float64 `db:"raw_score"`
	Country     string   `db:"country"`
	Flags       string   `db:"flags"`
}

func InsertAIResult(ctx context.Context, tx sqlx.ExtContext, r AIResult) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_ai_results
		(server_id, endpoint_key, generation, service, attempted_at, observed_at, ip, family, reachable, http_status, error, lookup_at, raw_score, country, flags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ServerID, r.EndpointKey, r.Generation, r.Service,
		r.AttemptedAt, r.ObservedAt, r.IP, r.Family, r.Reachable, r.HTTPStatus, r.Error, r.LookupAt, r.RawScore, r.Country, r.Flags)
	return err
}

// AIResults keeps each generation's latest attempt, success and observation,
// plus the last observation that can invalidate its success. Returning to an
// earlier exit after a failed lookup must not restore its old score.
func AIResults(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]AIResult, error) {
	var out []AIResult
	err := sqlx.SelectContext(ctx, q, &out, `SELECT * FROM servers_ai_results WHERE id IN (
		SELECT max(id) FROM servers_ai_results WHERE server_id = ? GROUP BY endpoint_key, generation, service
		UNION SELECT max(id) FROM servers_ai_results WHERE server_id = ? AND raw_score IS NOT NULL GROUP BY endpoint_key, generation, service
		UNION SELECT max(id) FROM servers_ai_results WHERE server_id = ? AND ip <> '' GROUP BY endpoint_key, generation, service
		UNION SELECT max(r.id) FROM servers_ai_results r WHERE r.server_id = ? AND r.ip <> '' AND r.ip <> (
			SELECT s.ip FROM servers_ai_results s WHERE s.server_id = r.server_id AND s.endpoint_key = r.endpoint_key
			AND s.generation = r.generation AND s.service = r.service AND s.raw_score IS NOT NULL ORDER BY s.id DESC LIMIT 1
		) GROUP BY r.endpoint_key, r.generation, r.service
	) ORDER BY id DESC`, serverID, serverID, serverID, serverID)
	return out, err
}

type AICache struct {
	IP         string  `db:"ip"`
	FetchedAt  db.Time `db:"fetched_at"`
	Reputation string  `db:"reputation"`
}

func GetAICache(ctx context.Context, q sqlx.QueryerContext, ip string) (AICache, bool, error) {
	var c AICache
	err := sqlx.GetContext(ctx, q, &c, `SELECT ip, fetched_at, reputation FROM servers_ai_cache WHERE ip = ?`, ip)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

func PutAICache(ctx context.Context, tx sqlx.ExtContext, c AICache) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_ai_cache (ip, fetched_at, reputation) VALUES (?, ?, ?)
		ON CONFLICT(ip) DO UPDATE SET fetched_at = excluded.fetched_at, reputation = excluded.reputation`, c.IP, c.FetchedAt, c.Reputation)
	return err
}

func AIBackoff(ctx context.Context, q sqlx.QueryerContext) (db.Time, error) {
	var at db.Time
	err := sqlx.GetContext(ctx, q, &at, `SELECT until_at FROM servers_ai_backoff WHERE id = 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return at, nil
	}
	return at, err
}

func SetAIBackoff(ctx context.Context, tx sqlx.ExtContext, at db.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO servers_ai_backoff (id, until_at) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET until_at = max(until_at, excluded.until_at)`, at)
	return err
}

func PruneAI(ctx context.Context, tx sqlx.ExtContext, before db.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM servers_ai_results WHERE attempted_at < ?`, before)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM servers_ai_cache WHERE fetched_at < ?`, before)
	if err != nil {
		return 0, err
	}
	m, err := res.RowsAffected()
	return n + m, err
}

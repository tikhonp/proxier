package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// FetchURL is a row of rscripts_fetch_urls without its token.
type FetchURL struct {
	ID           int64   `db:"id"`
	GenerationID int64   `db:"generation_id"`
	State        string  `db:"state"` // waiting, used, expired, replaced
	CreatedAt    db.Time `db:"created_at"`
	CreatedBy    string  `db:"created_by"`
	ExpiresAt    db.Time `db:"expires_at"`
	EndedAt      db.Time `db:"ended_at"` // zero while waiting
	IP           string  `db:"ip"`
	UserAgent    string  `db:"user_agent"`
}

const fetchURLSelect = `SELECT id, generation_id, state, created_at, created_by, expires_at,
	ended_at, ip, user_agent FROM rscripts_fetch_urls`

// InsertFetchURL adds a waiting URL with its token's lookup; the token
// itself is sealed with the new id (SetFetchToken), so it starts as X”.
func InsertFetchURL(ctx context.Context, x sqlx.ExtContext, u FetchURL, lookup []byte) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO rscripts_fetch_urls (generation_id, token, token_lookup, state, created_at, created_by, expires_at)
		VALUES (?, X'', ?, 'waiting', ?, ?, ?)`, u.GenerationID, lookup, u.CreatedAt, u.CreatedBy, u.ExpiresAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetFetchToken stores a waiting URL's sealed token.
func SetFetchToken(ctx context.Context, x sqlx.ExtContext, id int64, sealed []byte) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_fetch_urls SET token = ? WHERE id = ? AND state = 'waiting'`, sealed, id)
	return err
}

// FetchToken reads a waiting URL's sealed token.
func FetchToken(ctx context.Context, q sqlx.QueryerContext, id int64) ([]byte, error) {
	var b []byte
	err := sqlx.GetContext(ctx, q, &b, `SELECT token FROM rscripts_fetch_urls WHERE id = ? AND state = 'waiting'`, id)
	return b, notFound(err)
}

// WaitingFetchURL reads a generation's waiting URL, past its hour or not.
func WaitingFetchURL(ctx context.Context, q sqlx.QueryerContext, generationID int64) (FetchURL, error) {
	var u FetchURL
	err := sqlx.GetContext(ctx, q, &u, fetchURLSelect+` WHERE generation_id = ? AND state = 'waiting'`, generationID)
	return u, notFound(err)
}

// FetchURLByLookup reads the waiting URL whose token has this lookup and
// whose hour hasn't passed at now.
func FetchURLByLookup(ctx context.Context, q sqlx.QueryerContext, lookup []byte, now db.Time) (FetchURL, error) {
	var u FetchURL
	err := sqlx.GetContext(ctx, q, &u, fetchURLSelect+` WHERE token_lookup = ? AND state = 'waiting' AND expires_at > ?`, lookup, now)
	return u, notFound(err)
}

// EndFetchURL ends a waiting URL (replaced or expired) and erases its token.
func EndFetchURL(ctx context.Context, x sqlx.ExtContext, id int64, state string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_fetch_urls SET state = ?, ended_at = ?, token = NULL, token_lookup = NULL
		WHERE id = ? AND state = 'waiting'`, state, at, id)
	return err
}

// UseFetchURL marks a URL used by a fetch at now, if it still waits and its
// hour hasn't passed; false when another request used it first.
func UseFetchURL(ctx context.Context, x sqlx.ExtContext, id int64, now db.Time, ip, userAgent string) (bool, error) {
	res, err := x.ExecContext(ctx, `UPDATE rscripts_fetch_urls SET state = 'used', ended_at = ?, ip = ?, user_agent = ?, token = NULL, token_lookup = NULL
		WHERE id = ? AND state = 'waiting' AND expires_at > ?`, now, ip, userAgent, id, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpiredFetchURLs reads the waiting URLs whose hour has passed at now.
func ExpiredFetchURLs(ctx context.Context, q sqlx.QueryerContext, now db.Time) ([]FetchURL, error) {
	var out []FetchURL
	err := sqlx.SelectContext(ctx, q, &out, fetchURLSelect+` WHERE state = 'waiting' AND expires_at <= ? ORDER BY id`, now)
	return out, err
}

// FetchURLs reads a generation's URLs, newest first.
func FetchURLs(ctx context.Context, q sqlx.QueryerContext, generationID int64) ([]FetchURL, error) {
	var out []FetchURL
	err := sqlx.SelectContext(ctx, q, &out, fetchURLSelect+` WHERE generation_id = ? ORDER BY id DESC`, generationID)
	return out, err
}

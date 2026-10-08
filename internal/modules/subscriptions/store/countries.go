package store

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// How long a network's country is kept: a known one a month, an unknown one
// (lookups off, or the lookup failed) a day, so a network still in use is
// looked up again.
const (
	CountryFresh = 30 * 24 * time.Hour
	CountryRetry = 24 * time.Hour
)

// HasNetworkCountry reports whether the network has a row, known or not.
func HasNetworkCountry(ctx context.Context, q sqlx.QueryerContext, network string) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM subs_network_countries WHERE network = ?`, network)
	return n > 0, err
}

// NetworksToLookUp are networks seen in fetches at or after since that have
// no row, at most limit, by name.
func NetworksToLookUp(ctx context.Context, q sqlx.QueryerContext, since db.Time, limit int) ([]string, error) {
	var out []string
	err := sqlx.SelectContext(ctx, q, &out, `SELECT DISTINCT f.network FROM subs_fetches f
		WHERE f.at >= ? AND NOT EXISTS (SELECT 1 FROM subs_network_countries c WHERE c.network = f.network)
		ORDER BY f.network LIMIT ?`, since, limit)
	return out, err
}

// PutNetworkCountry stores a network's country ("" = unknown).
func PutNetworkCountry(ctx context.Context, x sqlx.ExtContext, network, country string, at db.Time) error {
	_, err := x.ExecContext(ctx, `INSERT INTO subs_network_countries (network, country, looked_up_at) VALUES (?, ?, ?)
		ON CONFLICT (network) DO UPDATE SET country = excluded.country, looked_up_at = excluded.looked_up_at`, network, country, at)
	return err
}

// PruneCountries deletes known countries looked up at or before fresh and
// unknown ones at or before retry.
func PruneCountries(ctx context.Context, x sqlx.ExtContext, fresh, retry db.Time) (int64, error) {
	res, err := x.ExecContext(ctx, `DELETE FROM subs_network_countries
		WHERE (country <> '' AND looked_up_at <= ?) OR (country = '' AND looked_up_at <= ?)`, fresh, retry)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

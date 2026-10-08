package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Fetch is a row of subs_fetches.
type Fetch struct {
	ID        int64   `db:"id"`
	LinkID    int64   `db:"link_id"`
	At        db.Time `db:"at"`
	IP        string  `db:"ip"`
	Network   string  `db:"network"`
	UserAgent string  `db:"user_agent"`
	App       string  `db:"app"`
	Format    string  `db:"format"`
	Outcome   string  `db:"outcome"`
	Country   string  `db:"country"` // of the network; "" unknown (read only)
}

// InsertFetch records one fetch.
func InsertFetch(ctx context.Context, x sqlx.ExtContext, f Fetch) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO subs_fetches (link_id, at, ip, network, user_agent, app, format, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, f.LinkID, f.At, f.IP, f.Network, f.UserAgent, f.App, f.Format, f.Outcome)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Fetches of a link, newest first, older than the fetch id before (0: from
// the newest), at most limit.
func Fetches(ctx context.Context, q sqlx.QueryerContext, linkID, before int64, limit int) ([]Fetch, error) {
	var out []Fetch
	query := `SELECT f.id, f.link_id, f.at, f.ip, f.network, f.user_agent, f.app, f.format, f.outcome,
		coalesce(c.country, '') AS country
		FROM subs_fetches f LEFT JOIN subs_network_countries c ON c.network = f.network WHERE f.link_id = ?`
	args := []any{linkID}
	if before > 0 {
		query += ` AND f.id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY f.id DESC LIMIT ?`
	args = append(args, limit)
	err := sqlx.SelectContext(ctx, q, &out, query, args...)
	return out, err
}

// FetchCountsSince counts each link's fetches at or after since.
func FetchCountsSince(ctx context.Context, q sqlx.QueryerContext, since db.Time) (map[int64]int, error) {
	var rows []struct {
		ID int64 `db:"link_id"`
		N  int   `db:"n"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT link_id, count(*) AS n FROM subs_fetches WHERE at >= ? GROUP BY link_id`, since); err != nil {
		return nil, err
	}
	out := map[int64]int{}
	for _, r := range rows {
		out[r.ID] = r.N
	}
	return out, nil
}

// appKey is what counts as one app: the family, or the user agent of an
// unknown one.
const appKey = `CASE WHEN app = '' THEN 'ua:' || user_agent ELSE app END`

// WindowCount is how many distinct networks and apps fetched a link.
type WindowCount struct {
	LinkID   int64 `db:"link_id"`
	Networks int   `db:"networks"`
	Apps     int   `db:"apps"`
}

// WindowCounts counts distinct networks and apps of each link's fetches in
// (since, until]; linkID 0 counts every link. A link without fetches there
// has no entry.
func WindowCounts(ctx context.Context, q sqlx.QueryerContext, since, until db.Time, linkID int64) ([]WindowCount, error) {
	query := `SELECT link_id, count(DISTINCT network) AS networks, count(DISTINCT ` + appKey + `) AS apps
		FROM subs_fetches WHERE at > ? AND at <= ?`
	args := []any{since, until}
	if linkID != 0 {
		query += ` AND link_id = ?`
		args = append(args, linkID)
	}
	var out []WindowCount
	err := sqlx.SelectContext(ctx, q, &out, query+` GROUP BY link_id ORDER BY link_id`, args...)
	return out, err
}

// Group is one network or app of a link's fetches.
type Group struct {
	Key     string `db:"k"`
	Country string `db:"country"`
	Fetches int    `db:"n"`
}

// FetchNetworks are the networks of a link's fetches after since, with their
// countries, most fetches first.
func FetchNetworks(ctx context.Context, q sqlx.QueryerContext, linkID int64, since db.Time) ([]Group, error) {
	var out []Group
	err := sqlx.SelectContext(ctx, q, &out, `SELECT f.network AS k, coalesce(c.country, '') AS country, count(*) AS n
		FROM subs_fetches f LEFT JOIN subs_network_countries c ON c.network = f.network
		WHERE f.link_id = ? AND f.at > ? GROUP BY f.network ORDER BY n DESC, k`, linkID, since)
	return out, err
}

// FetchApps are the apps of a link's fetches after since (a family, or
// "ua:<user agent>"), most fetches first.
func FetchApps(ctx context.Context, q sqlx.QueryerContext, linkID int64, since db.Time) ([]Group, error) {
	var out []Group
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+appKey+` AS k, '' AS country, count(*) AS n
		FROM subs_fetches WHERE link_id = ? AND at > ? GROUP BY k ORDER BY n DESC, k`, linkID, since)
	return out, err
}

// PruneFetches deletes fetches older than before.
func PruneFetches(ctx context.Context, x sqlx.ExtContext, before db.Time) (int64, error) {
	res, err := x.ExecContext(ctx, `DELETE FROM subs_fetches WHERE at < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

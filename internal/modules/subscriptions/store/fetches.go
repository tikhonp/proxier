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
	query := `SELECT id, link_id, at, ip, network, user_agent, app, format, outcome FROM subs_fetches WHERE link_id = ?`
	args := []any{linkID}
	if before > 0 {
		query += ` AND id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
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

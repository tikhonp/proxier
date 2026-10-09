package store

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Retention of the Shadowrocket fetch log (docs/data-model.md#retention).
const ShadowrocketFetchRetention = 90 * 24 * time.Hour

// Shadowrocket is a row of routing_shadowrocket with its list's name and its
// current base version. The token is never read with it.
type Shadowrocket struct {
	ID          int64   `db:"id"`
	Name        string  `db:"name"`
	ListID      int64   `db:"list_id"`
	List        string  `db:"list"`
	Policy      string  `db:"policy"`
	Enabled     bool    `db:"enabled"`
	CreatedAt   db.Time `db:"created_at"`
	LastFetchAt db.Time `db:"last_fetch_at"`
	LastFetchUA string  `db:"last_fetch_ua"`
	Version     int     `db:"version"`
}

const shadowrocketSelect = `SELECT c.id, c.name, c.list_id, l.name AS list, c.policy, c.enabled, c.created_at, c.last_fetch_at, c.last_fetch_ua,
	coalesce((SELECT max(v.number) FROM routing_shadowrocket_versions v WHERE v.config_id = c.id), 0) AS version
	FROM routing_shadowrocket c JOIN routing_lists l ON l.id = c.list_id`

// ShadowrocketConfigs reads every config by name.
func ShadowrocketConfigs(ctx context.Context, q sqlx.QueryerContext) ([]Shadowrocket, error) {
	var out []Shadowrocket
	err := sqlx.SelectContext(ctx, q, &out, shadowrocketSelect+` ORDER BY c.name`)
	return out, err
}

// ShadowrocketOfList reads the configs following a list, by name.
func ShadowrocketOfList(ctx context.Context, q sqlx.QueryerContext, listID int64) ([]Shadowrocket, error) {
	var out []Shadowrocket
	err := sqlx.SelectContext(ctx, q, &out, shadowrocketSelect+` WHERE c.list_id = ? ORDER BY c.name`, listID)
	return out, err
}

// GetShadowrocket reads one config.
func GetShadowrocket(ctx context.Context, q sqlx.QueryerContext, id int64) (Shadowrocket, error) {
	var c Shadowrocket
	err := sqlx.GetContext(ctx, q, &c, shadowrocketSelect+` WHERE c.id = ?`, id)
	return c, notFound(err)
}

// ShadowrocketByLookup finds the config holding a token, by its lookup.
func ShadowrocketByLookup(ctx context.Context, q sqlx.QueryerContext, lookup []byte) (Shadowrocket, error) {
	var c Shadowrocket
	err := sqlx.GetContext(ctx, q, &c, shadowrocketSelect+` WHERE c.token_lookup = ?`, lookup)
	return c, notFound(err)
}

// ShadowrocketNameTaken reports whether another config (not except) has the name.
func ShadowrocketNameTaken(ctx context.Context, q sqlx.QueryerContext, name string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM routing_shadowrocket WHERE name = ? AND id <> ?`, name, except)
	return n > 0, err
}

// InsertShadowrocket adds a config with an empty token (sealed right after
// with the id in its AAD) and returns its id.
func InsertShadowrocket(ctx context.Context, x sqlx.ExtContext, c Shadowrocket, lookup []byte) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO routing_shadowrocket (name, list_id, policy, token, token_lookup, enabled, created_at)
		VALUES (?, ?, ?, X'', ?, 1, ?)`, c.Name, c.ListID, c.Policy, lookup, c.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetShadowrocketToken writes a config's sealed token and its lookup.
func SetShadowrocketToken(ctx context.Context, x sqlx.ExtContext, id int64, sealed, lookup []byte) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_shadowrocket SET token = ?, token_lookup = ? WHERE id = ?`, sealed, lookup, id)
	return err
}

// ShadowrocketToken reads a config's sealed token.
func ShadowrocketToken(ctx context.Context, q sqlx.QueryerContext, id int64) ([]byte, error) {
	var b []byte
	err := sqlx.GetContext(ctx, q, &b, `SELECT token FROM routing_shadowrocket WHERE id = ?`, id)
	return b, notFound(err)
}

// SetShadowrocketFields writes a config's list and policy.
func SetShadowrocketFields(ctx context.Context, x sqlx.ExtContext, id, listID int64, policy string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_shadowrocket SET list_id = ?, policy = ? WHERE id = ?`, listID, policy, id)
	return err
}

// SetShadowrocketEnabled enables or disables a config.
func SetShadowrocketEnabled(ctx context.Context, x sqlx.ExtContext, id int64, enabled bool) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_shadowrocket SET enabled = ? WHERE id = ?`, enabled, id)
	return err
}

// DeleteShadowrocket deletes a config; its versions and fetches go with it
// (ON DELETE CASCADE).
func DeleteShadowrocket(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_shadowrocket WHERE id = ?`, id)
	return err
}

// ShadowrocketVersion is a row of routing_shadowrocket_versions.
type ShadowrocketVersion struct {
	Number    int     `db:"number"`
	Content   string  `db:"content"`
	Note      string  `db:"note"`
	CreatedAt db.Time `db:"created_at"`
}

// InsertShadowrocketVersion adds a base version.
func InsertShadowrocketVersion(ctx context.Context, x sqlx.ExtContext, configID int64, v ShadowrocketVersion) error {
	_, err := x.ExecContext(ctx, `INSERT INTO routing_shadowrocket_versions (config_id, number, content, note, created_at) VALUES (?, ?, ?, ?, ?)`,
		configID, v.Number, v.Content, v.Note, v.CreatedAt)
	return err
}

// ShadowrocketVersions reads a config's versions, newest first, without
// their content.
func ShadowrocketVersions(ctx context.Context, q sqlx.QueryerContext, configID int64) ([]ShadowrocketVersion, error) {
	var out []ShadowrocketVersion
	err := sqlx.SelectContext(ctx, q, &out, `SELECT number, '' AS content, note, created_at FROM routing_shadowrocket_versions
		WHERE config_id = ? ORDER BY number DESC`, configID)
	return out, err
}

// GetShadowrocketVersion reads one version; number 0 is the current one.
func GetShadowrocketVersion(ctx context.Context, q sqlx.QueryerContext, configID int64, number int) (ShadowrocketVersion, error) {
	var v ShadowrocketVersion
	var err error
	if number == 0 {
		err = sqlx.GetContext(ctx, q, &v, `SELECT number, content, note, created_at FROM routing_shadowrocket_versions
			WHERE config_id = ? ORDER BY number DESC LIMIT 1`, configID)
	} else {
		err = sqlx.GetContext(ctx, q, &v, `SELECT number, content, note, created_at FROM routing_shadowrocket_versions
			WHERE config_id = ? AND number = ?`, configID, number)
	}
	return v, notFound(err)
}

// ShadowrocketWithBase reads the configs whose current base is content.
func ShadowrocketWithBase(ctx context.Context, q sqlx.QueryerContext, content string) ([]string, error) {
	var out []string
	err := sqlx.SelectContext(ctx, q, &out, `SELECT c.name FROM routing_shadowrocket c
		JOIN routing_shadowrocket_versions v ON v.config_id = c.id
		WHERE v.number = (SELECT max(number) FROM routing_shadowrocket_versions w WHERE w.config_id = c.id) AND v.content = ?
		ORDER BY c.name`, content)
	return out, err
}

// ShadowrocketFetch is a row of routing_shadowrocket_fetches.
type ShadowrocketFetch struct {
	ID        int64   `db:"id"`
	ConfigID  int64   `db:"config_id"`
	At        db.Time `db:"at"`
	IP        string  `db:"ip"`
	UserAgent string  `db:"user_agent"`
}

// RecordShadowrocketFetch inserts a fetch and moves the config's last fetch.
func RecordShadowrocketFetch(ctx context.Context, x sqlx.ExtContext, f ShadowrocketFetch) error {
	if _, err := x.ExecContext(ctx, `INSERT INTO routing_shadowrocket_fetches (config_id, at, ip, user_agent) VALUES (?, ?, ?, ?)`,
		f.ConfigID, f.At, f.IP, f.UserAgent); err != nil {
		return err
	}
	_, err := x.ExecContext(ctx, `UPDATE routing_shadowrocket SET last_fetch_at = ?, last_fetch_ua = ? WHERE id = ?`, f.At, f.UserAgent, f.ConfigID)
	return err
}

// ShadowrocketFetches reads a config's fetches, newest first, with an id
// below before (0: from the newest).
func ShadowrocketFetches(ctx context.Context, q sqlx.QueryerContext, configID, before int64, limit int) ([]ShadowrocketFetch, error) {
	if before <= 0 {
		before = 1 << 62
	}
	var out []ShadowrocketFetch
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id, config_id, at, ip, user_agent FROM routing_shadowrocket_fetches
		WHERE config_id = ? AND id < ? ORDER BY id DESC LIMIT ?`, configID, before, limit)
	return out, err
}

// PruneShadowrocketFetches deletes at most limit fetches made before cutoff.
func PruneShadowrocketFetches(ctx context.Context, x sqlx.ExtContext, cutoff db.Time, limit int) (int64, error) {
	res, err := x.ExecContext(ctx, `DELETE FROM routing_shadowrocket_fetches WHERE id IN (
		SELECT id FROM routing_shadowrocket_fetches WHERE at < ? LIMIT ?)`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

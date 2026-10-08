package store

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Link is a row of subs_links without its token.
type Link struct {
	ID               int64          `db:"id"`
	SubscriptionID   sql.NullInt64  `db:"subscription_id"`
	Name             string         `db:"name"`
	Note             string         `db:"note"`
	State            string         `db:"state"`
	ExpiresAt        db.Time        `db:"expires_at"`
	Language         string         `db:"language"`
	Format           sql.NullString `db:"format"`
	AlertNetworks    sql.NullInt64  `db:"alert_networks"`
	AlertApps        sql.NullInt64  `db:"alert_apps"`
	AlertsMuted      bool           `db:"alerts_muted"`
	AlertedAt        db.Time        `db:"alerted_at"`
	ExpiryWarnedAt   db.Time        `db:"expiry_warned_at"`
	ExpiredAt        db.Time        `db:"expired_at"`
	CreatedAt        db.Time        `db:"created_at"`
	DisabledAt       db.Time        `db:"disabled_at"`
	DeletedAt        db.Time        `db:"deleted_at"`
	LastFetchAt      db.Time        `db:"last_fetch_at"`
	LastFetchApp     string         `db:"last_fetch_app"`
	LastFetchNetwork string         `db:"last_fetch_network"`
}

const linkCols = `id, subscription_id, name, note, state, expires_at, language, format, alert_networks, alert_apps,
	alerts_muted, alerted_at, expiry_warned_at, expired_at, created_at, disabled_at, deleted_at, last_fetch_at,
	last_fetch_app, last_fetch_network`

// InsertLink adds an active link with an empty token blob and its lookup; the
// caller seals the token with the new id (SetToken) in the same transaction.
func InsertLink(ctx context.Context, x sqlx.ExtContext, l Link, lookup []byte) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO subs_links (subscription_id, name, note, token, token_lookup, state, expires_at,
		language, created_at) VALUES (?, ?, ?, X'', ?, 'active', ?, ?, ?)`,
		l.SubscriptionID, l.Name, l.Note, lookup, l.ExpiresAt, l.Language, l.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetToken stores a sealed token and its lookup.
func SetToken(ctx context.Context, x sqlx.ExtContext, id int64, sealed, lookup []byte) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_links SET token = ?, token_lookup = ? WHERE id = ?`, sealed, lookup, id)
	return err
}

// Token reads a link's sealed token; nil once erased.
func Token(ctx context.Context, q sqlx.QueryerContext, id int64) ([]byte, error) {
	var b []byte
	err := sqlx.GetContext(ctx, q, &b, `SELECT token FROM subs_links WHERE id = ?`, id)
	return b, notFound(err)
}

// GetLink reads one by id.
func GetLink(ctx context.Context, q sqlx.QueryerContext, id int64) (Link, error) {
	var l Link
	err := sqlx.GetContext(ctx, q, &l, `SELECT `+linkCols+` FROM subs_links WHERE id = ?`, id)
	return l, notFound(err)
}

// LinkByLookup finds the link holding a token by its lookup.
func LinkByLookup(ctx context.Context, q sqlx.QueryerContext, lookup []byte) (Link, error) {
	var l Link
	err := sqlx.GetContext(ctx, q, &l, `SELECT `+linkCols+` FROM subs_links WHERE token_lookup = ?`, lookup)
	return l, notFound(err)
}

// ListLinks reads every link, by id.
func ListLinks(ctx context.Context, q sqlx.QueryerContext) ([]Link, error) {
	var out []Link
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+linkCols+` FROM subs_links ORDER BY id`)
	return out, err
}

// LinksOfSubscription reads the links of a subscription, deleted ones
// included, by name.
func LinksOfSubscription(ctx context.Context, q sqlx.QueryerContext, subID int64) ([]Link, error) {
	var out []Link
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+linkCols+` FROM subs_links WHERE subscription_id = ? ORDER BY name, id`, subID)
	return out, err
}

// LinkNameTaken reports whether a live link other than except has the name.
func LinkNameTaken(ctx context.Context, q sqlx.QueryerContext, name string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM subs_links WHERE name = ? AND state <> 'deleted' AND id <> ?`, name, except)
	return n > 0, err
}

// SetLinkState writes the state and its times.
func SetLinkState(ctx context.Context, x sqlx.ExtContext, id int64, state string, disabledAt, deletedAt db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_links SET state = ?, disabled_at = ?, deleted_at = ? WHERE id = ?`,
		state, disabledAt, deletedAt, id)
	return err
}

// SetLinkSubscription points a link at another subscription.
func SetLinkSubscription(ctx context.Context, x sqlx.ExtContext, id, subID int64) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_links SET subscription_id = ? WHERE id = ?`, subID, id)
	return err
}

// SetLinkExpiry writes the expiry and re-arms the warning and the expired
// notification for it.
func SetLinkExpiry(ctx context.Context, x sqlx.ExtContext, id int64, expires db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_links SET expires_at = ?, expiry_warned_at = NULL, expired_at = NULL WHERE id = ?`, expires, id)
	return err
}

// EditLink writes the plain fields.
func EditLink(ctx context.Context, x sqlx.ExtContext, id int64, name, note, language string, format sql.NullString) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_links SET name = ?, note = ?, language = ?, format = ? WHERE id = ?`,
		name, note, language, format, id)
	return err
}

// SetLastFetch records a link's last fetch.
func SetLastFetch(ctx context.Context, x sqlx.ExtContext, id int64, at db.Time, app, network string) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_links SET last_fetch_at = ?, last_fetch_app = ?, last_fetch_network = ? WHERE id = ?`,
		at, app, network, id)
	return err
}

// SetAllUnhealthy marks when subscription.all_unhealthy was last raised.
func SetAllUnhealthy(ctx context.Context, x sqlx.ExtContext, subID int64, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_subscriptions SET all_unhealthy_at = ? WHERE id = ?`, at, subID)
	return err
}

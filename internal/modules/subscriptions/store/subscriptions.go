package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Subscription is a row of subs_subscriptions.
type Subscription struct {
	ID             int64   `db:"id"`
	Name           string  `db:"name"`
	Title          string  `db:"title"`
	Description    string  `db:"description"`
	Formats        string  `db:"formats"`
	DefaultFormat  string  `db:"default_format"`
	UpdateHours    int     `db:"update_hours"`
	HideUnhealthy  bool    `db:"hide_unhealthy"`
	HideStates     string  `db:"hide_states"`
	HideGraceMin   int     `db:"hide_grace_min"`
	AutoAdd        bool    `db:"auto_add"`
	AutoAddSince   db.Time `db:"auto_add_since"`
	AllUnhealthyAt db.Time `db:"all_unhealthy_at"`
	CreatedAt      db.Time `db:"created_at"`
}

const subCols = `id, name, title, description, formats, default_format, update_hours, hide_unhealthy, hide_states,
	hide_grace_min, auto_add, auto_add_since, all_unhealthy_at, created_at`

// InsertSubscription adds one with the table's defaults.
func InsertSubscription(ctx context.Context, x sqlx.ExtContext, name, title, description string, at db.Time) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO subs_subscriptions (name, title, description, created_at) VALUES (?, ?, ?, ?)`,
		name, title, description, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetSubscription reads one by id.
func GetSubscription(ctx context.Context, q sqlx.QueryerContext, id int64) (Subscription, error) {
	var s Subscription
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+subCols+` FROM subs_subscriptions WHERE id = ?`, id)
	return s, notFound(err)
}

// ListSubscriptions reads every subscription, by name.
func ListSubscriptions(ctx context.Context, q sqlx.QueryerContext) ([]Subscription, error) {
	var out []Subscription
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+subCols+` FROM subs_subscriptions ORDER BY name, id`)
	return out, err
}

// NameTaken reports whether another subscription has the name (exact match).
func NameTaken(ctx context.Context, q sqlx.QueryerContext, name string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM subs_subscriptions WHERE name = ? AND id <> ?`, name, except)
	return n > 0, err
}

// UpdateSubscription writes every settings column of s.
func UpdateSubscription(ctx context.Context, x sqlx.ExtContext, s Subscription) error {
	_, err := x.ExecContext(ctx, `UPDATE subs_subscriptions SET name = ?, title = ?, description = ?, formats = ?, default_format = ?,
		update_hours = ?, hide_unhealthy = ?, hide_states = ?, hide_grace_min = ?, auto_add = ?, auto_add_since = ? WHERE id = ?`,
		s.Name, s.Title, s.Description, s.Formats, s.DefaultFormat, s.UpdateHours, s.HideUnhealthy, s.HideStates, s.HideGraceMin,
		s.AutoAdd, s.AutoAddSince, s.ID)
	return err
}

// DeleteSubscription removes it with its members; deleted links keep their
// rows with no subscription.
func DeleteSubscription(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM subs_subscriptions WHERE id = ?`, id)
	return err
}

// AutoAddSubscriptions are the subscriptions that add new servers, with the
// time the switch was turned on.
func AutoAddSubscriptions(ctx context.Context, q sqlx.QueryerContext) ([]Subscription, error) {
	var out []Subscription
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+subCols+` FROM subs_subscriptions WHERE auto_add = 1 ORDER BY id`)
	return out, err
}

// Member is a row of subs_subscription_servers.
type Member struct {
	SubscriptionID int64   `db:"subscription_id"`
	ServerID       int64   `db:"server_id"`
	ServerName     string  `db:"server_name"`
	Position       int     `db:"position"`
	AddedAt        db.Time `db:"added_at"`
}

// Members of a subscription, in order.
func Members(ctx context.Context, q sqlx.QueryerContext, subID int64) ([]Member, error) {
	var out []Member
	err := sqlx.SelectContext(ctx, q, &out, `SELECT subscription_id, server_id, server_name, position, added_at
		FROM subs_subscription_servers WHERE subscription_id = ? ORDER BY position`, subID)
	return out, err
}

// AllMembers of every subscription, by subscription and position.
func AllMembers(ctx context.Context, q sqlx.QueryerContext) ([]Member, error) {
	var out []Member
	err := sqlx.SelectContext(ctx, q, &out, `SELECT subscription_id, server_id, server_name, position, added_at
		FROM subs_subscription_servers ORDER BY subscription_id, position`)
	return out, err
}

// SubscriptionsOfServer are the ids of the subscriptions holding the server.
func SubscriptionsOfServer(ctx context.Context, q sqlx.QueryerContext, serverID int64) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT subscription_id FROM subs_subscription_servers WHERE server_id = ? ORDER BY subscription_id`, serverID)
	return out, err
}

// AddMember appends a server at position.
func AddMember(ctx context.Context, x sqlx.ExtContext, subID, serverID int64, name string, position int, at db.Time) error {
	_, err := x.ExecContext(ctx, `INSERT INTO subs_subscription_servers (subscription_id, server_id, server_name, position, added_at) VALUES (?, ?, ?, ?, ?)`,
		subID, serverID, name, position, at)
	return err
}

// RemoveMember deletes one member; the caller re-densifies the positions.
func RemoveMember(ctx context.Context, x sqlx.ExtContext, subID, serverID int64) (bool, error) {
	res, err := x.ExecContext(ctx, `DELETE FROM subs_subscription_servers WHERE subscription_id = ? AND server_id = ?`, subID, serverID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetPositions writes positions 1…n in the order of serverIDs.
func SetPositions(ctx context.Context, x sqlx.ExtContext, subID int64, serverIDs []int64) error {
	for i, id := range serverIDs {
		if _, err := x.ExecContext(ctx, `UPDATE subs_subscription_servers SET position = ? WHERE subscription_id = ? AND server_id = ?`,
			i+1, subID, id); err != nil {
			return err
		}
	}
	return nil
}

// LinkCounts are the links of each subscription that aren't deleted.
func LinkCounts(ctx context.Context, q sqlx.QueryerContext) (map[int64]int, error) {
	var rows []struct {
		ID int64 `db:"subscription_id"`
		N  int   `db:"n"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT subscription_id, count(*) AS n FROM subs_links
		WHERE state <> 'deleted' GROUP BY subscription_id`); err != nil {
		return nil, err
	}
	out := map[int64]int{}
	for _, r := range rows {
		out[r.ID] = r.N
	}
	return out, nil
}

// LinkRef is a link pointing to a subscription.
type LinkRef struct {
	ID        int64   `db:"id"`
	Name      string  `db:"name"`
	State     string  `db:"state"`
	DeletedAt db.Time `db:"deleted_at"`
}

// LinksOf are every link pointing to the subscription, deleted ones included,
// by name.
func LinksOf(ctx context.Context, q sqlx.QueryerContext, subID int64) ([]LinkRef, error) {
	var out []LinkRef
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id, name, state, deleted_at FROM subs_links WHERE subscription_id = ? ORDER BY name, id`, subID)
	return out, err
}

// ActiveLinks counts the active, unexpired links of the subscriptions.
func ActiveLinks(ctx context.Context, q sqlx.QueryerContext, subIDs []int64, now db.Time) (int, error) {
	if len(subIDs) == 0 {
		return 0, nil
	}
	query, args, err := sqlx.In(`SELECT count(*) FROM subs_links WHERE subscription_id IN (?) AND state = 'active'
		AND (expires_at IS NULL OR expires_at > ?)`, subIDs, now)
	if err != nil {
		return 0, err
	}
	var n int
	err = sqlx.GetContext(ctx, q, &n, query, args...)
	return n, err
}

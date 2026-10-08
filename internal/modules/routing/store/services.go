package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Service is a row of routing_services.
type Service struct {
	ID              int64   `db:"id"`
	Tag             string  `db:"tag"`
	Source          string  `db:"source"`
	Selector        string  `db:"selector"`
	Name            string  `db:"name"`
	Description     string  `db:"description"`
	Origin          string  `db:"origin"`
	LastCheckedAt   db.Time `db:"last_checked_at"`
	LastError       string  `db:"last_error"`
	Failures        int     `db:"failures"`
	FailingNotified bool    `db:"failing_notified"`
	CreatedAt       db.Time `db:"created_at"`
}

const serviceCols = `id, tag, source, selector, name, description, origin, last_checked_at, last_error, failures,
	failing_notified, created_at`

// InsertService adds a service and returns its id.
func InsertService(ctx context.Context, x sqlx.ExtContext, s Service) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO routing_services (tag, source, selector, name, description, origin, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, s.Tag, s.Source, s.Selector, s.Name, s.Description, s.Origin, s.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetService reads one by id.
func GetService(ctx context.Context, q sqlx.QueryerContext, id int64) (Service, error) {
	var s Service
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+serviceCols+` FROM routing_services WHERE id = ?`, id)
	return s, notFound(err)
}

// ServiceByTag reads one by tag.
func ServiceByTag(ctx context.Context, q sqlx.QueryerContext, tag string) (Service, error) {
	var s Service
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+serviceCols+` FROM routing_services WHERE tag = ?`, tag)
	return s, notFound(err)
}

// ServiceRow is a service with its accepted snapshot's counts, for lists.
type ServiceRow struct {
	Service
	SuffixCount int     `db:"suffix_count"`
	ExactCount  int     `db:"exact_count"`
	AcceptedAt  db.Time `db:"accepted_at"`
}

// ListServices reads every service with its accepted counts, by tag; source
// "" means any.
func ListServices(ctx context.Context, q sqlx.QueryerContext, source string) ([]ServiceRow, error) {
	var out []ServiceRow
	err := sqlx.SelectContext(ctx, q, &out, `
		SELECT s.id, s.tag, s.source, s.selector, s.name, s.description, s.origin, s.last_checked_at, s.last_error,
			s.failures, s.failing_notified, s.created_at,
			coalesce(a.suffix_count, 0) AS suffix_count, coalesce(a.exact_count, 0) AS exact_count, a.accepted_at
		FROM routing_services s
		LEFT JOIN routing_snapshots a ON a.service_id = s.id AND a.status = 'accepted'
		WHERE ? = '' OR s.source = ?
		ORDER BY s.tag`, source, source)
	return out, err
}

// SetSource switches an upstream service's source.
func SetSource(ctx context.Context, x sqlx.ExtContext, id int64, source, selector string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_services SET source = ?, selector = ? WHERE id = ?`, source, selector, id)
	return err
}

// SetCustomFields writes a custom service's name, tag and description.
func SetCustomFields(ctx context.Context, x sqlx.ExtContext, id int64, name, tag, description string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_services SET name = ?, tag = ?, description = ? WHERE id = ?`, name, tag, description, id)
	return err
}

// DeleteService deletes a service; its snapshots and custom rows go with it.
// The foreign key refuses it while a list holds the service.
func DeleteService(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM routing_services WHERE id = ?`, id)
	return err
}

// TagTaken reports whether a service other than except has the tag.
func TagTaken(ctx context.Context, q sqlx.QueryerContext, tag string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM routing_services WHERE tag = ? AND id <> ?`, tag, except)
	return n > 0, err
}

// CustomNameTaken reports whether a custom service other than except has the name.
func CustomNameTaken(ctx context.Context, q sqlx.QueryerContext, name string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM routing_services WHERE source = 'custom' AND name = ? AND id <> ?`, name, except)
	return n > 0, err
}

// CountServices counts every service and the custom ones.
func CountServices(ctx context.Context, q sqlx.QueryerContext) (all, custom int, err error) {
	var c struct {
		All    int `db:"a"`
		Custom int `db:"c"`
	}
	err = sqlx.GetContext(ctx, q, &c, `SELECT count(*) AS a, coalesce(sum(source = 'custom'), 0) AS c FROM routing_services`)
	return c.All, c.Custom, err
}

// CustomDomain is a row of routing_custom_domains.
type CustomDomain struct {
	Domain string `db:"domain"`
	Exact  bool   `db:"exact"`
	Note   string `db:"note"`
}

// CustomDomains reads a custom service's rows, by domain.
func CustomDomains(ctx context.Context, q sqlx.QueryerContext, id int64) ([]CustomDomain, error) {
	var out []CustomDomain
	err := sqlx.SelectContext(ctx, q, &out, `SELECT domain, exact, note FROM routing_custom_domains WHERE service_id = ? ORDER BY domain`, id)
	return out, err
}

// ReplaceCustomDomains writes a custom service's rows.
func ReplaceCustomDomains(ctx context.Context, x sqlx.ExtContext, id int64, rows []CustomDomain) error {
	if _, err := x.ExecContext(ctx, `DELETE FROM routing_custom_domains WHERE service_id = ?`, id); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := x.ExecContext(ctx, `INSERT INTO routing_custom_domains (service_id, domain, exact, note) VALUES (?, ?, ?, ?)`,
			id, r.Domain, r.Exact, r.Note); err != nil {
			return err
		}
	}
	return nil
}

// ServiceBySelector reads the upstream service stored with that selector.
func ServiceBySelector(ctx context.Context, q sqlx.QueryerContext, sel string) (Service, error) {
	var s Service
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+serviceCols+` FROM routing_services WHERE selector = ? AND source <> 'custom' ORDER BY id LIMIT 1`, sel)
	return s, notFound(err)
}

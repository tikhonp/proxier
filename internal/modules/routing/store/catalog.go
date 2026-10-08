package store

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// CatalogSource is a row of routing_catalog_sources.
type CatalogSource struct {
	Source       string  `db:"source"`
	Generation   int64   `db:"generation"`
	RefreshedAt  db.Time `db:"refreshed_at"`
	Revision     string  `db:"revision"`
	Entries      int     `db:"entries"`
	Failures     int     `db:"failures"`
	FailingSince db.Time `db:"failing_since"`
	LastError    string  `db:"last_error"`
	Notified     bool    `db:"notified"`
}

const catalogSourceCols = `source, generation, refreshed_at, revision, entries, failures, failing_since, last_error, notified`

// CatalogSources reads the four sources in their fixed order.
func CatalogSources(ctx context.Context, q sqlx.QueryerContext) ([]CatalogSource, error) {
	var out []CatalogSource
	err := sqlx.SelectContext(ctx, q, &out, `SELECT `+catalogSourceCols+` FROM routing_catalog_sources
		ORDER BY CASE source WHEN 'v2fly' THEN 0 WHEN 'iplist:main' THEN 1 WHEN 'iplist:beta' THEN 2 ELSE 3 END`)
	return out, err
}

// GetCatalogSource reads one source.
func GetCatalogSource(ctx context.Context, q sqlx.QueryerContext, source string) (CatalogSource, error) {
	var s CatalogSource
	err := sqlx.GetContext(ctx, q, &s, `SELECT `+catalogSourceCols+` FROM routing_catalog_sources WHERE source = ?`, source)
	return s, notFound(err)
}

// CatalogUnchanged records a refresh that found the source as it was: its
// generation stays and its failures end.
func CatalogUnchanged(ctx context.Context, x sqlx.ExtContext, source string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_catalog_sources SET refreshed_at = ?, failures = 0, last_error = '',
		failing_since = NULL, notified = 0 WHERE source = ?`, at, source)
	return err
}

// CatalogFlip puts a written generation in force.
func CatalogFlip(ctx context.Context, x sqlx.ExtContext, source string, generation int64, revision string, entries int, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_catalog_sources SET generation = ?, refreshed_at = ?, revision = ?, entries = ?,
		failures = 0, last_error = '', failing_since = NULL, notified = 0 WHERE source = ?`, generation, at, revision, entries, source)
	return err
}

// CatalogFailed counts a failed refresh of a source.
func CatalogFailed(ctx context.Context, x sqlx.ExtContext, source, errText string, at db.Time) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_catalog_sources SET failures = failures + 1, last_error = ?,
		failing_since = coalesce(failing_since, ?) WHERE source = ?`, errText, at, source)
	return err
}

// SetCatalogNotified records that catalog_refresh_failed was sent for this run.
func SetCatalogNotified(ctx context.Context, x sqlx.ExtContext, source string) error {
	_, err := x.ExecContext(ctx, `UPDATE routing_catalog_sources SET notified = 1 WHERE source = ?`, source)
	return err
}

// CatalogEntry is a row of routing_catalog_entries.
type CatalogEntry struct {
	Source     string `db:"source"`
	Generation int64  `db:"generation"`
	Kind       string `db:"kind"`
	Name       string `db:"name"`
	Group      string `db:"grp"`
	Sites      int    `db:"sites"`
	Domains    int    `db:"domains"`
}

// CatalogDomain is a row of routing_catalog_domains.
type CatalogDomain struct {
	Source     string `db:"source"`
	Generation int64  `db:"generation"`
	Name       string `db:"name"`
	Domain     string `db:"domain"`
	Exact      bool   `db:"exact"`
	Attrs      string `db:"attrs"`
}

// CatalogInclude is a row of routing_catalog_includes (v2fly).
type CatalogInclude struct {
	Generation int64  `db:"generation"`
	List       string `db:"list"`
	Included   string `db:"included"`
	Filter     string `db:"filter"`
}

// InsertCatalogEntries writes entries.
func InsertCatalogEntries(ctx context.Context, x sqlx.ExtContext, rows []CatalogEntry) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := sqlx.NamedExecContext(ctx, x, `INSERT OR IGNORE INTO routing_catalog_entries (source, generation, kind, name, grp, sites, domains)
		VALUES (:source, :generation, :kind, :name, :grp, :sites, :domains)`, rows)
	return err
}

// InsertCatalogDomains writes reverse-index rows.
func InsertCatalogDomains(ctx context.Context, x sqlx.ExtContext, rows []CatalogDomain) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := sqlx.NamedExecContext(ctx, x, `INSERT INTO routing_catalog_domains (source, generation, name, domain, exact, attrs)
		VALUES (:source, :generation, :name, :domain, :exact, :attrs)`, rows)
	return err
}

// InsertCatalogIncludes writes v2fly include rows.
func InsertCatalogIncludes(ctx context.Context, x sqlx.ExtContext, rows []CatalogInclude) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := sqlx.NamedExecContext(ctx, x, `INSERT OR IGNORE INTO routing_catalog_includes (generation, list, included, filter)
		VALUES (:generation, :list, :included, :filter)`, rows)
	return err
}

// DeleteStaleCatalog deletes at most limit rows of a source whose generation
// isn't keep (only older ones with olderOnly: a newer one may be being
// written), table by table, and returns how many it deleted: a caller loops
// until 0, one short transaction each. The includes are v2fly's.
func DeleteStaleCatalog(ctx context.Context, x sqlx.ExtContext, source string, keep int64, olderOnly bool, limit int) (int64, error) {
	op := "<>"
	if olderOnly {
		op = "<"
	}
	var total int64
	for _, stmt := range []string{
		`DELETE FROM routing_catalog_domains WHERE rowid IN (SELECT rowid FROM routing_catalog_domains WHERE source = ? AND generation ` + op + ` ? LIMIT ?)`,
		`DELETE FROM routing_catalog_entries WHERE rowid IN (SELECT rowid FROM routing_catalog_entries WHERE source = ? AND generation ` + op + ` ? LIMIT ?)`,
	} {
		res, err := x.ExecContext(ctx, stmt, source, keep, limit)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if source == "v2fly" {
		res, err := x.ExecContext(ctx, `DELETE FROM routing_catalog_includes WHERE rowid IN
			(SELECT rowid FROM routing_catalog_includes WHERE generation `+op+` ? LIMIT ?)`, keep, limit)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// SearchRow is an entry of a source's current generation, with that
// source's age.
type SearchRow struct {
	CatalogEntry
	RefreshedAt db.Time `db:"refreshed_at"`
}

// CatalogSearch finds entries of the current generations whose name holds q
// (lowercase): v2fly lists, then iplist groups, then sites, each by name and
// then portal. It returns at most limit rows and how many match in all.
func CatalogSearch(ctx context.Context, q sqlx.QueryerContext, text, source, kind, portal string, limit int) ([]SearchRow, int, error) {
	where := []string{"instr(e.name, ?) > 0"}
	args := []any{text}
	switch source {
	case "v2fly":
		where = append(where, "e.source = 'v2fly'")
	case "iplist":
		where = append(where, "e.source LIKE 'iplist:%'")
	}
	if kind != "" {
		where = append(where, "e.kind = ?")
		args = append(args, kind)
	}
	if portal != "" {
		where = append(where, "e.source = ?")
		args = append(args, "iplist:"+portal)
	}
	from := ` FROM routing_catalog_entries e
		JOIN routing_catalog_sources s ON s.source = e.source AND s.generation = e.generation
		WHERE ` + strings.Join(where, " AND ")
	var n int
	if err := sqlx.GetContext(ctx, q, &n, `SELECT count(*)`+from, args...); err != nil {
		return nil, 0, err
	}
	var out []SearchRow
	err := sqlx.SelectContext(ctx, q, &out, `SELECT e.source, e.generation, e.kind, e.name, e.grp, e.sites, e.domains, s.refreshed_at`+from+`
		ORDER BY CASE e.kind WHEN 'list' THEN 0 WHEN 'group' THEN 1 ELSE 2 END, e.name,
			CASE e.source WHEN 'iplist:main' THEN 1 WHEN 'iplist:beta' THEN 2 WHEN 'iplist:russia' THEN 3 ELSE 0 END
		LIMIT ?`, append(args, limit)...)
	return out, n, err
}

// CatalogCounts counts the entries of each source's current generation.
func CatalogCounts(ctx context.Context, q sqlx.QueryerContext) (map[string]int, error) {
	var rows []struct {
		Source string `db:"source"`
		N      int    `db:"n"`
	}
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT e.source, count(*) AS n FROM routing_catalog_entries e
		JOIN routing_catalog_sources s ON s.source = e.source AND s.generation = e.generation GROUP BY e.source`); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.Source] = r.N
	}
	return out, nil
}

// IplistNamed reads, for each name, the (source, kind) pairs of the current
// iplist generations that hold a site or group of that name.
func IplistNamed(ctx context.Context, q sqlx.QueryerContext, names []string) ([]CatalogEntry, error) {
	if len(names) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(`SELECT e.source, e.generation, e.kind, e.name, e.grp, e.sites, e.domains FROM routing_catalog_entries e
		JOIN routing_catalog_sources s ON s.source = e.source AND s.generation = e.generation
		WHERE e.source LIKE 'iplist:%' AND e.name IN (?)`, names)
	if err != nil {
		return nil, err
	}
	var out []CatalogEntry
	err = sqlx.SelectContext(ctx, q, &out, query, args...)
	return out, err
}

// Holding is a catalog entry that holds a domain directly.
type Holding struct {
	Source string `db:"source"`
	Name   string `db:"name"`
	Domain string `db:"domain"`
}

// CatalogHolders reads the lists and sites of the current generations that
// hold any of the domains directly.
func CatalogHolders(ctx context.Context, q sqlx.QueryerContext, domains []string) ([]Holding, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(`SELECT DISTINCT d.source, d.name, d.domain FROM routing_catalog_domains d
		JOIN routing_catalog_sources s ON s.source = d.source AND s.generation = d.generation
		WHERE d.domain IN (?)`, domains)
	if err != nil {
		return nil, err
	}
	var out []Holding
	err = sqlx.SelectContext(ctx, q, &out, query, args...)
	return out, err
}

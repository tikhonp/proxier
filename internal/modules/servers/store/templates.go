package store

import (
	"context"
	"errors"
	"sort"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// ErrSlugTaken is returned when a template with that slug exists.
var ErrSlugTaken = errors.New("slug taken")

// Template is the row of a template.
type Template struct {
	ID             int64   `db:"id"`
	Slug           string  `db:"slug"`
	Name           string  `db:"name"`
	Description    string  `db:"description"`
	DefaultVersion int     `db:"default_version"` // 0 before the first publish
	ArchivedAt     db.Time `db:"archived_at"`
	CreatedAt      db.Time `db:"created_at"`
}

const templateSelect = `SELECT id, slug, name, description, COALESCE(default_version, 0) AS default_version, archived_at, created_at FROM servers_templates`

// InsertTemplate adds a template with no versions.
func InsertTemplate(ctx context.Context, tx sqlx.ExtContext, slug, name, description string, at db.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO servers_templates (slug, name, description, created_at) VALUES (?, ?, ?, ?)`, slug, name, description, at)
	if unique(err, "servers_templates.slug") {
		return 0, ErrSlugTaken
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetTemplate returns one template.
func GetTemplate(ctx context.Context, q sqlx.QueryerContext, id int64) (Template, error) {
	var t Template
	return t, notFound(sqlx.GetContext(ctx, q, &t, templateSelect+` WHERE id = ?`, id))
}

// ListTemplates returns every template by name.
func ListTemplates(ctx context.Context, q sqlx.QueryerContext) ([]Template, error) {
	var out []Template
	err := sqlx.SelectContext(ctx, q, &out, templateSelect+` ORDER BY name, id`)
	return out, err
}

// CountTemplates counts every template, archived ones included.
func CountTemplates(ctx context.Context, q sqlx.QueryerContext) (int, error) {
	var n int
	return n, sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM servers_templates`)
}

// SetDefaultVersion makes number the version new servers are built from.
func SetDefaultVersion(ctx context.Context, tx sqlx.ExtContext, id int64, number int) error {
	_, err := tx.ExecContext(ctx, `UPDATE servers_templates SET default_version = ? WHERE id = ?`, number, id)
	return err
}

// DraftMeta is a draft without its files.
type DraftMeta struct {
	BasedOn   int     `db:"based_on"` // 0 = none
	Revision  int     `db:"revision"`
	Source    string  `db:"source"` // JSON
	UpdatedAt db.Time `db:"updated_at"`
	UpdatedBy string  `db:"updated_by"`
}

// GetDraft returns a template's draft without its files, ErrNotFound when it has none.
func GetDraft(ctx context.Context, q sqlx.QueryerContext, id int64) (DraftMeta, error) {
	var d DraftMeta
	return d, notFound(sqlx.GetContext(ctx, q, &d,
		`SELECT COALESCE(based_on, 0) AS based_on, revision, source, updated_at, updated_by FROM servers_template_drafts WHERE template_id = ?`, id))
}

// DraftFiles returns the files of a template's draft.
func DraftFiles(ctx context.Context, q sqlx.QueryerContext, id int64) (map[string][]byte, error) {
	return readFiles(ctx, q, `SELECT path, content FROM servers_draft_files WHERE template_id = ?`, id)
}

func readFiles(ctx context.Context, q sqlx.QueryerContext, query string, id int64) (map[string][]byte, error) {
	rows, err := q.QueryxContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]byte{}
	for rows.Next() {
		var p string
		var c []byte
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		out[p] = c
	}
	return out, rows.Err()
}

// InsertDraft creates a draft at revision 1. basedOn 0 means a new template.
func InsertDraft(ctx context.Context, tx sqlx.ExtContext, id int64, basedOn int, source, by string, at db.Time, files map[string][]byte) error {
	var based any
	if basedOn > 0 {
		based = basedOn
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO servers_template_drafts (template_id, based_on, revision, source, updated_at, updated_by) VALUES (?, ?, 1, ?, ?, ?)`,
		id, based, source, at, by); err != nil {
		return err
	}
	return putDraftFiles(ctx, tx, id, files)
}

// ReplaceDraft stores a new revision of the draft with exactly these files.
func ReplaceDraft(ctx context.Context, tx sqlx.ExtContext, id int64, revision int, by string, at db.Time, files map[string][]byte) error {
	if _, err := tx.ExecContext(ctx, `UPDATE servers_template_drafts SET revision = ?, updated_at = ?, updated_by = ? WHERE template_id = ?`, revision, at, by, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers_draft_files WHERE template_id = ?`, id); err != nil {
		return err
	}
	return putDraftFiles(ctx, tx, id, files)
}

func putDraftFiles(ctx context.Context, tx sqlx.ExtContext, id int64, files map[string][]byte) error {
	for _, p := range sortedKeys(files) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers_draft_files (template_id, path, content) VALUES (?, ?, ?)`, id, p, files[p]); err != nil {
			return err
		}
	}
	return nil
}

// DeleteDraft removes the draft and its files.
func DeleteDraft(ctx context.Context, tx sqlx.ExtContext, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM servers_template_drafts WHERE template_id = ?`, id)
	return err
}

// VersionRow is a published version without its files.
type VersionRow struct {
	ID          int64   `db:"id"`
	TemplateID  int64   `db:"template_id"`
	Number      int     `db:"number"`
	Notes       string  `db:"notes"`
	Warnings    string  `db:"warnings"` // JSON
	Source      string  `db:"source"`   // JSON
	PublishedAt db.Time `db:"published_at"`
	PublishedBy string  `db:"published_by"`
}

const versionSelect = `SELECT id, template_id, number, notes, warnings, source, published_at, published_by FROM servers_template_versions`

// LatestVersion is the highest version number of a template, 0 for none.
func LatestVersion(ctx context.Context, q sqlx.QueryerContext, id int64) (int, error) {
	var n int
	return n, sqlx.GetContext(ctx, q, &n, `SELECT COALESCE(MAX(number), 0) FROM servers_template_versions WHERE template_id = ?`, id)
}

// GetVersion returns one version, ErrNotFound when there is none.
func GetVersion(ctx context.Context, q sqlx.QueryerContext, id int64, number int) (VersionRow, error) {
	var v VersionRow
	return v, notFound(sqlx.GetContext(ctx, q, &v, versionSelect+` WHERE template_id = ? AND number = ?`, id, number))
}

// ListVersions returns a template's versions, newest first.
func ListVersions(ctx context.Context, q sqlx.QueryerContext, id int64) ([]VersionRow, error) {
	var out []VersionRow
	err := sqlx.SelectContext(ctx, q, &out, versionSelect+` WHERE template_id = ? ORDER BY number DESC`, id)
	return out, err
}

// VersionFiles returns the files of a version row.
func VersionFiles(ctx context.Context, q sqlx.QueryerContext, versionID int64) (map[string][]byte, error) {
	return readFiles(ctx, q, `SELECT path, content FROM servers_template_files WHERE version_id = ?`, versionID)
}

// InsertVersion adds a version with its files and returns its row id.
func InsertVersion(ctx context.Context, tx sqlx.ExtContext, v VersionRow, files map[string][]byte) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO servers_template_versions (template_id, number, notes, warnings, source, published_at, published_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.TemplateID, v.Number, v.Notes, v.Warnings, v.Source, v.PublishedAt, v.PublishedBy)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, p := range sortedKeys(files) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers_template_files (version_id, path, content) VALUES (?, ?, ?)`, id, p, files[p]); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

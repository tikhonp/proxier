package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Script is a row of rscripts_scripts; Current is 0 before the first version.
type Script struct {
	ID          int64   `db:"id"`
	Name        string  `db:"name"`
	Slug        string  `db:"slug"`
	Description string  `db:"description"`
	Current     int     `db:"current"`
	Archived    bool    `db:"archived"`
	CreatedAt   db.Time `db:"created_at"`
}

const scriptSelect = `SELECT id, name, slug, description, coalesce(current_version, 0) AS current, archived, created_at FROM rscripts_scripts`

// GetScript reads one script.
func GetScript(ctx context.Context, q sqlx.QueryerContext, id int64) (Script, error) {
	var s Script
	err := sqlx.GetContext(ctx, q, &s, scriptSelect+` WHERE id = ?`, id)
	return s, notFound(err)
}

// Scripts reads the scripts that are (or aren't) archived, by name.
func Scripts(ctx context.Context, q sqlx.QueryerContext, archived bool) ([]Script, error) {
	var out []Script
	err := sqlx.SelectContext(ctx, q, &out, scriptSelect+` WHERE archived = ? ORDER BY name`, archived)
	return out, err
}

// ArchivedCount counts the archived scripts.
func ArchivedCount(ctx context.Context, q sqlx.QueryerContext) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM rscripts_scripts WHERE archived = 1`)
	return n, err
}

// Taken reports whether another script (not except) has the value in column
// ("name" or "slug").
func Taken(ctx context.Context, q sqlx.QueryerContext, column, value string, except int64) (bool, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM rscripts_scripts WHERE `+column+` = ? AND id <> ?`, value, except)
	return n > 0, err
}

// InsertScript adds a script without a version and returns its id.
func InsertScript(ctx context.Context, x sqlx.ExtContext, s Script) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO rscripts_scripts (name, slug, description, created_at) VALUES (?, ?, ?, ?)`,
		s.Name, s.Slug, s.Description, s.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateScript writes a script's name, slug and description.
func UpdateScript(ctx context.Context, x sqlx.ExtContext, id int64, name, slug, description string) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_scripts SET name = ?, slug = ?, description = ? WHERE id = ?`, name, slug, description, id)
	return err
}

// SetCurrent points a script at one of its versions.
func SetCurrent(ctx context.Context, x sqlx.ExtContext, id int64, version int) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_scripts SET current_version = ? WHERE id = ?`, nullable(version), id)
	return err
}

// SetArchived archives or unarchives a script.
func SetArchived(ctx context.Context, x sqlx.ExtContext, id int64, archived bool) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_scripts SET archived = ? WHERE id = ?`, archived, id)
	return err
}

// DeleteScript deletes a script; its versions and draft go with it (ON
// DELETE CASCADE), and a generation's version reference refuses it.
func DeleteScript(ctx context.Context, x sqlx.ExtContext, id int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM rscripts_scripts WHERE id = ?`, id)
	return err
}

// Version is a row of rscripts_versions.
type Version struct {
	Number      int     `db:"number"`
	Body        string  `db:"body"`
	SHA256      string  `db:"sha256"`
	Warnings    string  `db:"warnings"` // JSON
	Notes       string  `db:"notes"`
	PublishedAt db.Time `db:"published_at"`
	PublishedBy string  `db:"published_by"`
}

// InsertVersion adds a version.
func InsertVersion(ctx context.Context, x sqlx.ExtContext, scriptID int64, v Version) error {
	_, err := x.ExecContext(ctx, `INSERT INTO rscripts_versions (script_id, number, body, sha256, warnings, notes, published_at, published_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, scriptID, v.Number, v.Body, v.SHA256, v.Warnings, v.Notes, v.PublishedAt, v.PublishedBy)
	return err
}

// Versions reads a script's versions newest first, without their bodies.
func Versions(ctx context.Context, q sqlx.QueryerContext, scriptID int64) ([]Version, error) {
	var out []Version
	err := sqlx.SelectContext(ctx, q, &out, `SELECT number, '' AS body, sha256, warnings, notes, published_at, published_by
		FROM rscripts_versions WHERE script_id = ? ORDER BY number DESC`, scriptID)
	return out, err
}

// GetVersion reads one version with its body.
func GetVersion(ctx context.Context, q sqlx.QueryerContext, scriptID int64, number int) (Version, error) {
	var v Version
	err := sqlx.GetContext(ctx, q, &v, `SELECT number, body, sha256, warnings, notes, published_at, published_by
		FROM rscripts_versions WHERE script_id = ? AND number = ?`, scriptID, number)
	return v, notFound(err)
}

// LastVersion is a script's highest version number, 0 for none.
func LastVersion(ctx context.Context, q sqlx.QueryerContext, scriptID int64) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT coalesce(max(number), 0) FROM rscripts_versions WHERE script_id = ?`, scriptID)
	return n, err
}

// PublishedAt is when each script's current version was published.
func PublishedAt(ctx context.Context, q sqlx.QueryerContext) (map[int64]db.Time, error) {
	var rows []struct {
		ID int64   `db:"id"`
		At db.Time `db:"published_at"`
	}
	err := sqlx.SelectContext(ctx, q, &rows, `SELECT s.id, v.published_at FROM rscripts_scripts s
		JOIN rscripts_versions v ON v.script_id = s.id AND v.number = s.current_version`)
	out := make(map[int64]db.Time, len(rows))
	for _, r := range rows {
		out[r.ID] = r.At
	}
	return out, err
}

// Draft is a row of rscripts_drafts; BasedOn is 0 for none.
type Draft struct {
	ScriptID  int64   `db:"script_id"`
	Body      string  `db:"body"`
	BasedOn   int     `db:"based_on"`
	Revision  int     `db:"revision"`
	UpdatedAt db.Time `db:"updated_at"`
	UpdatedBy string  `db:"updated_by"`
}

const draftSelect = `SELECT script_id, body, coalesce(based_on, 0) AS based_on, revision, updated_at, updated_by FROM rscripts_drafts`

// GetDraft reads a script's draft.
func GetDraft(ctx context.Context, q sqlx.QueryerContext, scriptID int64) (Draft, error) {
	var d Draft
	err := sqlx.GetContext(ctx, q, &d, draftSelect+` WHERE script_id = ?`, scriptID)
	return d, notFound(err)
}

// Drafts reads every draft, by script.
func Drafts(ctx context.Context, q sqlx.QueryerContext) (map[int64]Draft, error) {
	var rows []Draft
	err := sqlx.SelectContext(ctx, q, &rows, draftSelect)
	out := make(map[int64]Draft, len(rows))
	for _, d := range rows {
		out[d.ScriptID] = d
	}
	return out, err
}

// InsertDraft adds a script's draft.
func InsertDraft(ctx context.Context, x sqlx.ExtContext, d Draft) error {
	_, err := x.ExecContext(ctx, `INSERT INTO rscripts_drafts (script_id, body, based_on, revision, updated_at, updated_by) VALUES (?, ?, ?, ?, ?, ?)`,
		d.ScriptID, d.Body, nullable(d.BasedOn), d.Revision, d.UpdatedAt, d.UpdatedBy)
	return err
}

// UpdateDraft writes a draft's body and revision.
func UpdateDraft(ctx context.Context, x sqlx.ExtContext, d Draft) error {
	_, err := x.ExecContext(ctx, `UPDATE rscripts_drafts SET body = ?, revision = ?, updated_at = ?, updated_by = ? WHERE script_id = ?`,
		d.Body, d.Revision, d.UpdatedAt, d.UpdatedBy, d.ScriptID)
	return err
}

// DeleteDraft deletes a script's draft.
func DeleteDraft(ctx context.Context, x sqlx.ExtContext, scriptID int64) error {
	_, err := x.ExecContext(ctx, `DELETE FROM rscripts_drafts WHERE script_id = ?`, scriptID)
	return err
}

// Generations counts the generations made from a script.
func Generations(ctx context.Context, q sqlx.QueryerContext, scriptID int64) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, q, &n, `SELECT count(*) FROM rscripts_generations WHERE script_id = ?`, scriptID)
	return n, err
}

// GenerationCounts counts the generations of every script that has any.
func GenerationCounts(ctx context.Context, q sqlx.QueryerContext) (map[int64]int, error) {
	var rows []struct {
		ID int64 `db:"script_id"`
		N  int   `db:"n"`
	}
	err := sqlx.SelectContext(ctx, q, &rows, `SELECT script_id, count(*) AS n FROM rscripts_generations GROUP BY script_id`)
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.ID] = r.N
	}
	return out, err
}

// VersionGenerations counts a script's generations per version.
func VersionGenerations(ctx context.Context, q sqlx.QueryerContext, scriptID int64) (map[int]int, error) {
	var rows []struct {
		V int `db:"version"`
		N int `db:"n"`
	}
	err := sqlx.SelectContext(ctx, q, &rows, `SELECT version, count(*) AS n FROM rscripts_generations WHERE script_id = ? GROUP BY version`, scriptID)
	out := make(map[int]int, len(rows))
	for _, r := range rows {
		out[r.V] = r.N
	}
	return out, err
}

package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Generation is a row of rscripts_generations; LinkID and RouterID are 0
// for none.
type Generation struct {
	ID               int64   `db:"id"`
	OwnerID          int64   `db:"owner_id"`
	ScriptID         int64   `db:"script_id"`
	Version          int     `db:"version"`
	RouterName       string  `db:"router_name"`
	FileName         string  `db:"file_name"`
	Vals             string  `db:"vals"` // JSON {name: value}
	Secrets          []byte  `db:"secrets"`
	Changed          string  `db:"changed"`
	LinkID           int64   `db:"link_id"`
	LinkName         string  `db:"link_name"`
	LinkCreated      bool    `db:"link_created"`
	RouterID         int64   `db:"router_id"`
	RouterRegistered bool    `db:"router_registered"`
	KeyFingerprint   string  `db:"key_fingerprint"`
	CreatedAt        db.Time `db:"created_at"`
	CreatedBy        string  `db:"created_by"`
}

const generationSelect = `SELECT id, owner_id, script_id, version, router_name, file_name, vals, secrets, changed,
	coalesce(link_id, 0) AS link_id, link_name, link_created, coalesce(router_id, 0) AS router_id, router_registered,
	key_fingerprint, created_at, created_by FROM rscripts_generations`

// InsertGeneration adds a generation with its script, version, names and
// creation; UpdateGeneration writes the rest once its id is known (the
// secrets' AAD names it).
func InsertGeneration(ctx context.Context, x sqlx.ExtContext, g Generation) (int64, error) {
	res, err := x.ExecContext(ctx, `INSERT INTO rscripts_generations (owner_id, script_id, version, router_name, file_name, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, g.OwnerID, g.ScriptID, g.Version, g.RouterName, g.FileName, g.CreatedAt, g.CreatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// UpdateGeneration writes a generation's values, link, router and key.
func UpdateGeneration(ctx context.Context, x sqlx.ExtContext, g Generation) error {
	var secrets any
	if len(g.Secrets) > 0 {
		secrets = g.Secrets
	}
	_, err := x.ExecContext(ctx, `UPDATE rscripts_generations SET vals = ?, secrets = ?, changed = ?, link_id = ?, link_name = ?,
		link_created = ?, router_id = ?, router_registered = ?, key_fingerprint = ? WHERE id = ?`,
		g.Vals, secrets, g.Changed, nullID(g.LinkID), g.LinkName, g.LinkCreated, nullID(g.RouterID), g.RouterRegistered, g.KeyFingerprint, g.ID)
	return err
}

// GetGeneration reads one generation within scope.
func GetGeneration(ctx context.Context, q sqlx.QueryerContext, id int64, scope access.Scope) (Generation, error) {
	var g Generation
	err := sqlx.GetContext(ctx, q, &g, generationSelect+` WHERE id = ? AND ? IN (0, owner_id)`, id, scope)
	return g, notFound(err)
}

// ScriptGenerations reads a script's generations within scope, newest
// first.
func ScriptGenerations(ctx context.Context, q sqlx.QueryerContext, scriptID int64, scope access.Scope) ([]Generation, error) {
	var out []Generation
	err := sqlx.SelectContext(ctx, q, &out, generationSelect+` WHERE script_id = ? AND ? IN (0, owner_id) ORDER BY id DESC`, scriptID, scope)
	return out, err
}

// AllGenerations reads every generation within scope, newest first
// (search).
func AllGenerations(ctx context.Context, q sqlx.QueryerContext, scope access.Scope) ([]Generation, error) {
	var out []Generation
	err := sqlx.SelectContext(ctx, q, &out, generationSelect+` WHERE ? IN (0, owner_id) ORDER BY id DESC`, scope)
	return out, err
}

// HandOver gives every generation of from to to.
func HandOver(ctx context.Context, x sqlx.ExtContext, from, to int64) (int64, error) {
	res, err := x.ExecContext(ctx, `UPDATE rscripts_generations SET owner_id = ? WHERE owner_id = ?`, to, from)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OwnedGenerations are the ids of owner's generations.
func OwnedGenerations(ctx context.Context, q sqlx.QueryerContext, owner int64) ([]int64, error) {
	var out []int64
	err := sqlx.SelectContext(ctx, q, &out, `SELECT id FROM rscripts_generations WHERE owner_id = ? ORDER BY id`, owner)
	return out, err
}

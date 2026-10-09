package shadowrocket

import (
	"context"
	"errors"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// ErrNoVersion: the config has no such base version.
var ErrNoVersion = errors.New("shadowrocket: no such version")

// SaveBase makes a new base version when content differs from the current
// one. Errors are store.FieldErrors (base, note); an unchanged base makes
// no version and records nothing.
func (s *Service) SaveBase(ctx context.Context, id int64, content, note, actor string) (int, bool, error) {
	fe := store.FieldErrors{}
	checkBase(content, fe)
	note = checkNote(note, fe)
	if len(fe) > 0 {
		return 0, false, fe
	}
	return s.newVersion(ctx, id, content, note, actor)
}

func (s *Service) newVersion(ctx context.Context, id int64, content, note, actor string) (int, bool, error) {
	var version int
	changed := false
	err := s.change(ctx, id, func(tx *sqlx.Tx, _ store.Shadowrocket) error {
		cur, err := store.GetShadowrocketVersion(ctx, tx, id, 0)
		if err != nil {
			return err
		}
		version = cur.Number
		if cur.Content == content {
			return nil
		}
		version, changed = cur.Number+1, true
		if err := store.InsertShadowrocketVersion(ctx, tx, id, store.ShadowrocketVersion{
			Number: version, Content: content, Note: note, CreatedAt: db.At(s.d.Now()),
		}); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.shadowrocket_updated", id, actor, map[string]any{"changes": "base", "version": version})
	})
	return version, changed, err
}

// Restore makes a new version with version number's content (note
// "restored from vN"); restoring the current content makes nothing.
func (s *Service) Restore(ctx context.Context, id int64, number int, actor string) (int, error) {
	v, err := s.Version(ctx, id, number)
	if err != nil {
		return 0, err
	}
	n, _, err := s.newVersion(ctx, id, v.Content, "restored from v"+strconv.Itoa(number), actor)
	return n, err
}

// Versions reads a config's versions, newest first, without content.
func (s *Service) Versions(ctx context.Context, id int64) ([]Version, error) {
	rows, err := store.ShadowrocketVersions(ctx, s.d.DB.R, id)
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(rows))
	for _, r := range rows {
		out = append(out, Version{Number: r.Number, Note: r.Note, CreatedAt: r.CreatedAt.Time})
	}
	return out, nil
}

// Version reads one version with its content; 0 is the current one.
func (s *Service) Version(ctx context.Context, id int64, number int) (Version, error) {
	r, err := store.GetShadowrocketVersion(ctx, s.d.DB.R, id, number)
	if errors.Is(err, store.ErrNotFound) {
		return Version{}, ErrNoVersion
	}
	if err != nil {
		return Version{}, err
	}
	return Version{Number: r.Number, Content: r.Content, Note: r.Note, CreatedAt: r.CreatedAt.Time}, nil
}

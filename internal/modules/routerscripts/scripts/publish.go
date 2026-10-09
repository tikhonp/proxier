package scripts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Publish makes the draft (at revision) version N+1 and the current one.
// Errors refuse it (*PublishError{Blocked: true}); warnings need confirm
// (*PublishError otherwise). The confirmed warnings are kept with the
// version; nothing else derived from the body is stored.
func (s *Service) Publish(ctx context.Context, id int64, revision int, notes string, confirm bool, actor string) (int, error) {
	var number int
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := script(ctx, tx, id); err != nil {
			return err
		}
		d, err := store.GetDraft(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNoDraft
		}
		if err != nil {
			return err
		}
		if d.Revision != revision {
			return ErrDraftChanged
		}
		notes = strings.TrimSpace(notes)
		if utf8.RuneCountInString(notes) > MaxNotes {
			return store.FieldErrors{"notes": "scripts.err.notes"}
		}
		p, fs, err := s.report(ctx, tx, id, d.Body, d.BasedOn)
		if err != nil {
			return err
		}
		var errs, warns []params.Finding
		for _, f := range fs {
			if f.Severity == params.Error {
				errs = append(errs, f)
			} else {
				warns = append(warns, f)
			}
		}
		if len(errs) > 0 {
			return &PublishError{Findings: fs, Blocked: true}
		}
		if len(warns) > 0 && !confirm {
			return &PublishError{Findings: warns}
		}
		last, err := store.LastVersion(ctx, tx, id)
		if err != nil {
			return err
		}
		number = last + 1
		if warns == nil {
			warns = []params.Finding{}
		}
		wj, err := json.Marshal(warns)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(d.Body))
		v := store.Version{
			Number: number, Body: d.Body, SHA256: hex.EncodeToString(sum[:]), Warnings: string(wj), Notes: notes,
			PublishedAt: db.At(s.d.Now()), PublishedBy: actor,
		}
		if err := store.InsertVersion(ctx, tx, id, v); err != nil {
			return err
		}
		if err := store.SetCurrent(ctx, tx, id, number); err != nil {
			return err
		}
		if err := store.DeleteDraft(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "routerscript.version_published", id, actor, map[string]any{
			"version": number, "parameters": len(p.Params), "warnings": len(warns),
		})
	})
	return number, err
}

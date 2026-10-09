package scripts

import (
	"context"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

func toDraft(d store.Draft) Draft {
	return Draft{Body: d.Body, BasedOn: d.BasedOn, Revision: d.Revision, UpdatedAt: d.UpdatedAt.Time, UpdatedBy: d.UpdatedBy}
}

// Draft reads a script's draft (ErrNoDraft).
func (s *Service) Draft(ctx context.Context, id int64) (Draft, error) {
	d, err := store.GetDraft(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Draft{}, ErrNoDraft
	}
	return toDraft(d), err
}

// script reads a script in a transaction.
func script(ctx context.Context, tx *sqlx.Tx, id int64) (store.Script, error) {
	sc, err := store.GetScript(ctx, tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return sc, ErrNotFound
	}
	return sc, err
}

// EditDraft opens the script's draft, or makes one from the current version
// (ErrNoVersion when there is neither).
func (s *Service) EditDraft(ctx context.Context, id int64, actor string) (Draft, error) {
	var out Draft
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		sc, err := script(ctx, tx, id)
		if err != nil {
			return err
		}
		d, err := store.GetDraft(ctx, tx, id)
		if err == nil {
			out = toDraft(d)
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if sc.Current == 0 {
			return ErrNoVersion
		}
		v, err := store.GetVersion(ctx, tx, id, sc.Current)
		if err != nil {
			return err
		}
		d = store.Draft{ScriptID: id, Body: v.Body, BasedOn: sc.Current, Revision: 1, UpdatedAt: db.At(s.d.Now()), UpdatedBy: actor}
		if err := store.InsertDraft(ctx, tx, d); err != nil {
			return err
		}
		out = toDraft(d)
		return s.record(ctx, tx, "routerscript.draft_saved", id, actor, map[string]any{"based_on": sc.Current})
	})
	return out, err
}

// SaveDraft replaces the draft's body when revision is still the draft's
// (ErrDraftChanged otherwise). body already has the stored body's line
// endings (Normalize). The same body records nothing and keeps the revision.
func (s *Service) SaveDraft(ctx context.Context, id int64, revision int, body, actor string) (int, error) {
	rev := revision
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
		fe := store.FieldErrors{}
		if checkBody(body, fe); len(fe) > 0 {
			return fe
		}
		if d.Body == body {
			return nil
		}
		d.Body, d.Revision, d.UpdatedAt, d.UpdatedBy = body, d.Revision+1, db.At(s.d.Now()), actor
		if err := store.UpdateDraft(ctx, tx, d); err != nil {
			return err
		}
		rev = d.Revision
		return s.record(ctx, tx, "routerscript.draft_saved", id, actor, map[string]any{"based_on": d.BasedOn})
	})
	return rev, err
}

// DiscardDraft drops the draft. A script with no version would be left with
// nothing, so it is refused there (ErrNoVersion): delete the script instead.
func (s *Service) DiscardDraft(ctx context.Context, id int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		sc, err := script(ctx, tx, id)
		if err != nil {
			return err
		}
		if sc.Current == 0 {
			return ErrNoVersion
		}
		d, err := store.GetDraft(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNoDraft
		}
		if err != nil {
			return err
		}
		if err := store.DeleteDraft(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "routerscript.draft_discarded", id, actor, map[string]any{"based_on": d.BasedOn})
	})
}

// Normalize gives posted text the line endings of the body it replaces:
// browsers send a text area with CRLF, so without this saving from the
// editor would change every line of an LF script, or of a CRLF one.
func Normalize(posted, stored string) string {
	lf := strings.ReplaceAll(posted, "\r\n", "\n")
	first, _, found := strings.Cut(stored, "\n")
	if found && strings.HasSuffix(first, "\r") {
		return strings.ReplaceAll(lf, "\n", "\r\n")
	}
	return lf
}

// Report is the draft parsed, and its findings plus a warning per parameter
// of the version it is based on that is gone.
func (s *Service) Report(ctx context.Context, id int64) (params.Script, []params.Finding, error) {
	d, err := s.Draft(ctx, id)
	if err != nil {
		return params.Script{}, nil, err
	}
	p, fs, err := s.report(ctx, s.d.DB.R, id, d.Body, d.BasedOn)
	return p, fs, err
}

// ReportFor is Report for a body that isn't saved (the panel).
func (s *Service) ReportFor(ctx context.Context, id int64, body string, basedOn int) (params.Script, []params.Finding, error) {
	return s.report(ctx, s.d.DB.R, id, body, basedOn)
}

func (s *Service) report(ctx context.Context, q sqlx.QueryerContext, id int64, body string, basedOn int) (params.Script, []params.Finding, error) {
	p := params.Parse([]byte(body))
	fs := append([]params.Finding(nil), p.Findings...)
	if basedOn > 0 {
		v, err := store.GetVersion(ctx, q, id, basedOn)
		if err != nil {
			return p, nil, err
		}
		fs = append(fs, params.Gone(params.Parse([]byte(v.Body)), basedOn, p)...)
	}
	return p, fs, nil
}

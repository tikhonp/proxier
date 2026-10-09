package mtvpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// JobImport is the import job.
const JobImport = "routing.import"

type payload struct {
	ImportID int64 `json:"import_id"`
}

// JobTypes is routing.import: one attempt; a restart resumes it, and every
// row that already has an outcome is skipped.
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{{
		Name: JobImport, Queue: jobs.Refresh, MaxAttempts: 1,
		Steps: []jobs.Step{
			{Name: "services", Run: s.stepServices},
			{Name: "lists", Run: s.stepLists},
			{Name: "shadowrocket", Run: s.stepShadowrocket},
		},
		OnFailed: s.onFailed,
		OnCancelled: func(ctx context.Context, tx *sqlx.Tx, j jobs.Info, _ string) error {
			return store.FailImportOfJob(ctx, tx, j.ID, db.At(s.d.Now()))
		},
	}}
}

// onFailed marks the import failed and still records job.failed, which the
// import has no failure event of its own to replace.
func (s *Service) onFailed(ctx context.Context, tx *sqlx.Tx, j jobs.Info, cause error) error {
	if err := store.FailImportOfJob(ctx, tx, j.ID, db.At(s.d.Now())); err != nil {
		return err
	}
	text := cause.Error()
	if len(text) > 500 {
		text = text[:500]
	}
	_, err := s.d.Events.Record(ctx, tx, events.Event{
		Type: "job.failed", Actor: j.Actor(), Subject: events.Subject{Type: "job", ID: fmt.Sprint(j.ID)},
		Payload: map[string]any{"type": j.Type, "error": text},
	})
	return err
}

func (s *Service) load(ctx context.Context, q sqlx.QueryerContext, r *jobs.Run) (Import, error) {
	var p payload
	if err := r.Payload(&p); err != nil {
		return Import{}, jobs.Permanent(err)
	}
	row, err := store.GetImport(ctx, q, p.ImportID)
	if errors.Is(err, store.ErrNotFound) {
		return Import{}, jobs.Permanent(ErrNotFound)
	}
	if err != nil {
		return Import{}, err
	}
	return fromRow(row)
}

func saveRows(ctx context.Context, tx *sqlx.Tx, im Import) error {
	b, err := json.Marshal(im.Rows)
	if err != nil {
		return err
	}
	return store.SaveImportRows(ctx, tx, im.ID, string(b))
}

// stepServices resolves and creates each ticked row in order, each in its
// own Write with its outcome.
func (s *Service) stepServices(ctx context.Context, r *jobs.Run) error {
	im, err := s.load(ctx, s.d.DB.R, r)
	if err != nil {
		return err
	}
	actor := r.Info().Actor()
	log := r.Log()
	for i := range im.Rows {
		row := &im.Rows[i]
		if !row.Include || row.Outcome != "" {
			continue
		}
		if err := s.importRow(ctx, im, row, actor); err != nil {
			return err
		}
		if row.Outcome == Failed {
			log.Warn("%s: %s", row.Tag, row.Error)
		} else {
			log.Info("%s: %s", row.Tag, row.Outcome)
		}
	}
	return nil
}

// importRow gives one row its outcome and saves it. Only an error that
// isn't the row's (the database, a shutdown) is returned, so a resume
// takes the row again.
func (s *Service) importRow(ctx context.Context, im Import, row *Row, actor string) error {
	save := func(create func(tx *sqlx.Tx) error) error {
		return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			if create != nil {
				if err := create(tx); err != nil {
					return err
				}
			}
			return saveRows(ctx, tx, im)
		})
	}
	if ex, err := store.ServiceByTag(ctx, s.d.DB.R, row.Tag); err == nil {
		row.Outcome, row.ServiceID = Reused, ex.ID
		return save(nil)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	sel, err := selector.Parse(row.Selector)
	if err != nil {
		row.Outcome, row.Error = Failed, selectorProblem(err)
		return save(nil)
	}
	res, err := s.d.Resolver.Resolve(ctx, sel)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && res.Set.Count() == 0 {
		err = services.ErrEmptyResolve
	}
	if err != nil {
		row.Outcome, row.Error = Failed, failText(err)
		return save(nil)
	}
	create := func(tx *sqlx.Tx) error {
		var id int64
		var err error
		if row.Convert {
			id, err = s.d.Services.CreateCustomTx(ctx, tx, services.Custom{Name: row.Tag, Tag: row.Tag, Origin: "import"}, res.Set, actor)
			row.Outcome = Converted
		} else {
			pinned, perr := selector.Parse(sel.Pin(res.Portal).String())
			if perr != nil {
				return perr
			}
			id, err = s.d.Services.CreateTx(ctx, tx, services.Prepared{Selector: pinned, Tag: row.Tag, Resolved: res}, actor)
			row.Outcome = Created
		}
		row.ServiceID = id
		return err
	}
	err = save(create)
	var fe store.FieldErrors
	var tt *services.TagTakenError
	switch {
	case errors.As(err, &tt):
		row.Outcome, row.ServiceID, row.Error = Reused, tt.Existing.ID, ""
		return save(nil)
	case errors.As(err, &fe):
		row.Outcome, row.ServiceID = Failed, 0
		for _, v := range fe {
			row.Error = v
			break
		}
		return save(nil)
	case err != nil:
		row.Outcome, row.ServiceID = "", 0
	}
	return err
}

// failText is why a selector couldn't be imported.
func failText(err error) string {
	if errors.Is(err, services.ErrEmptyResolve) {
		return "mtvpn.err.empty_resolve"
	}
	return sources.ErrorText(err)
}

// stepLists adds the created, reused and converted services that pass the
// guard to the list in row order, in one Write.
func (s *Service) stepLists(ctx context.Context, r *jobs.Run) error {
	im, err := s.load(ctx, s.d.DB.R, r)
	if err != nil {
		return err
	}
	actor := r.Info().Actor()
	if im.ListID == 0 {
		r.Log().Warn("the routing list was deleted: nothing added")
		return nil
	}
	var ids []int64
	for i := range im.Rows {
		row := &im.Rows[i]
		if row.ServiceID == 0 || row.Outcome != Created && row.Outcome != Reused && row.Outcome != Converted {
			continue
		}
		snap, err := s.d.Services.Accepted(ctx, row.ServiceID)
		if errors.Is(err, services.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		err = s.d.Lists.CheckSet(ctx, row.Tag, snap.Set, []int64{im.ListID})
		var ge *lists.GuardError
		if errors.As(err, &ge) {
			row.Outcome = Skipped
			row.Guard = &GuardHit{Domain: ge.Domain, Server: ge.Server, Hostname: ge.Hostname}
			if rerr := s.d.Lists.RecordRefusal(ctx, ge, ge.ListIDs, actor); rerr != nil {
				return rerr
			}
			r.Log().Warn("%s: skipped, %s covers %s (%s)", row.Tag, ge.Domain, ge.Hostname, ge.Server)
			continue
		}
		if err != nil {
			return err
		}
		ids = append(ids, row.ServiceID)
	}
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := s.d.Lists.AppendTx(ctx, tx, im.ListID, ids, actor); err != nil {
			return err
		}
		return saveRows(ctx, tx, im)
	})
	if errors.Is(err, lists.ErrNotFound) || errors.Is(err, store.ErrNotFound) {
		r.Log().Warn("the routing list was deleted: nothing added")
		return nil
	}
	if err != nil {
		// a hostname appeared meanwhile: the refusal is recorded after the rollback
		s.d.Lists.Refused(ctx, err, actor)
	}
	return err
}

// stepShadowrocket creates the config, or records why not; then the import is done.
func (s *Service) stepShadowrocket(ctx context.Context, r *jobs.Run) error {
	im, err := s.load(ctx, s.d.DB.R, r)
	if err != nil {
		return err
	}
	sr := &im.Shadowrocket
	finish := func(tx *sqlx.Tx) error {
		b, err := json.Marshal(sr)
		if err != nil {
			return err
		}
		if err := store.SaveImportShadowrocket(ctx, tx, im.ID, string(b)); err != nil {
			return err
		}
		return store.FinishImport(ctx, tx, im.ID, "done", db.At(s.d.Now()))
	}
	if !sr.Create || sr.Outcome != "" {
		return s.d.DB.Write(ctx, finish)
	}
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		id, err := s.d.Shadowrocket.CreateTx(ctx, tx, shadowrocket.New{Name: sr.Name, ListID: sr.ListID, Policy: shadowrocket.DefaultPolicy,
			Base: im.base, Note: "imported from mtvpn"}, r.Info().Actor())
		if err != nil {
			return err
		}
		sr.Outcome, sr.ID = Created, id
		return finish(tx)
	})
	var fe store.FieldErrors
	if !errors.As(err, &fe) {
		return err
	}
	sr.Outcome, sr.ID = Failed, 0
	for _, k := range []string{"name", "list", "base", "policy", "note"} {
		if fe[k] != "" {
			sr.Error = fe[k]
			break
		}
	}
	r.Log().Warn("the Shadowrocket config wasn't created: %s", sr.Error)
	return s.d.DB.Write(ctx, finish)
}

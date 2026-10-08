package templates

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/validate"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// Publish turns the draft into the next version and removes the draft.
// Validation runs first, before the transaction. With errors nothing is
// written and *ValidationError is returned; with only warnings, they must be
// confirmed (*WarningsError otherwise). A revision that is no longer the
// draft's — at the start or by the time of the write — is ErrDraftChanged.
// The first version becomes the default.
func (s *Service) Publish(ctx context.Context, templateID int64, revision int, notes string, confirmWarnings bool, actor string) (version int, err error) {
	d, err := s.Draft(ctx, templateID)
	if err != nil {
		return 0, err
	}
	if d.Revision != revision {
		return 0, ErrDraftChanged
	}
	report, err := s.validate(ctx, templateID, d.Files)
	if err != nil {
		return 0, err
	}
	if !report.OK() {
		return 0, &ValidationError{report}
	}
	if len(report.Warnings()) > 0 && !confirmWarnings {
		return 0, &WarningsError{report}
	}

	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		meta, err := store.GetDraft(ctx, tx, templateID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrNoDraft
			}
			return err
		}
		if meta.Revision != revision {
			return ErrDraftChanged
		}
		version, err = s.insertVersion(ctx, tx, templateID, d.Files, notes, report.Warnings(), d.Source, actor)
		if err != nil {
			return err
		}
		if err := store.DeleteDraft(ctx, tx, templateID); err != nil {
			return err
		}
		return s.draftEnded(ctx, tx, templateID, "published", actor)
	})
	return version, err
}

// insertVersion writes version N+1 with its files, makes it the default when
// it is the first, and records the events. It runs in the caller's
// transaction.
func (s *Service) insertVersion(ctx context.Context, tx *sqlx.Tx, templateID int64, files map[string][]byte, notes string, warnings []finding.Finding, src Source, actor string) (int, error) {
	latest, err := store.LatestVersion(ctx, tx, templateID)
	if err != nil {
		return 0, err
	}
	number := latest + 1
	if warnings == nil {
		warnings = []finding.Finding{}
	}
	wj, _ := json.Marshal(warnings)
	if _, err := store.InsertVersion(ctx, tx, store.VersionRow{
		TemplateID: templateID, Number: number, Notes: notes, Warnings: string(wj), Source: src.json(),
		PublishedAt: s.now(), PublishedBy: actor,
	}, files); err != nil {
		return 0, err
	}
	messages := make([]string, len(warnings))
	for i, w := range warnings {
		messages[i] = w.Message
	}
	if _, err := s.ev.Record(ctx, tx, events.Event{Type: "template.version_published", Subject: subject(templateID), Actor: actor,
		Payload: map[string]any{"version": number, "warnings": len(warnings), "messages": messages}}); err != nil {
		return 0, err
	}
	if latest == 0 {
		if err := store.SetDefaultVersion(ctx, tx, templateID, number); err != nil {
			return 0, err
		}
		if _, err := s.ev.Record(ctx, tx, events.Event{Type: "template.default_version_changed", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"from": 0, "to": number}}); err != nil {
			return 0, err
		}
	}
	return number, nil
}

// SeedSpec is a template created already published, once.
type SeedSpec struct {
	Slug, Name, Description, Notes string
	Files                          map[string][]byte
	// MarkerKey is the servers_meta key that records the seed ran, so
	// deleting the template later does not bring it back.
	MarkerKey string
}

// Seed creates the template as version 1 (the default) when no template
// exists and the seed never ran. Warnings are accepted: the seed is trusted.
// It reports whether it created anything.
func (s *Service) Seed(ctx context.Context, spec SeedSpec, actor string) (bool, error) {
	if _, ok, err := store.MetaGet(ctx, s.d.R, spec.MarkerKey); err != nil || ok {
		return false, err
	}
	if n, err := store.CountTemplates(ctx, s.d.R); err != nil || n > 0 {
		return false, err
	}
	report := validate.Validate(ctx, validate.Input{Slug: spec.Slug, Files: spec.Files})
	if !report.OK() {
		return false, &ValidationError{report}
	}
	created := false
	err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
		// Again inside the transaction: nothing may slip in between.
		if _, ok, err := store.MetaGet(ctx, tx, spec.MarkerKey); err != nil || ok {
			return err
		}
		if n, err := store.CountTemplates(ctx, tx); err != nil || n > 0 {
			return err
		}
		id, err := store.InsertTemplate(ctx, tx, spec.Slug, spec.Name, spec.Description, s.now())
		if err != nil {
			return err
		}
		if _, err := s.ev.Record(ctx, tx, events.Event{Type: "template.created", Subject: subject(id), Actor: actor,
			Payload: map[string]any{"slug": spec.Slug}}); err != nil {
			return err
		}
		if _, err := s.insertVersion(ctx, tx, id, spec.Files, spec.Notes, report.Warnings(), Source{}, actor); err != nil {
			return err
		}
		created = true
		return store.MetaSet(ctx, tx, spec.MarkerKey, s.now().Format("2006-01-02T15:04:05.000Z"))
	})
	return created && err == nil, err
}

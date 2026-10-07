package templates

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/validate"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// maxEventFiles caps the file names an event carries.
const maxEventFiles = 50

// SaveDraft stores a new revision of the draft with exactly these files. It
// carries the revision it was based on: another save in between (a second
// browser tab, an agent) makes it ErrDraftChanged and nothing is written. A
// save that changes no file records nothing and keeps the revision. by is
// "admin" or "agent".
func (s *Service) SaveDraft(ctx context.Context, templateID int64, revision int, files map[string][]byte, by, actor string) (newRevision int, err error) {
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
		have, err := store.DraftFiles(ctx, tx, templateID)
		if err != nil {
			return err
		}
		changed := changedPaths(have, files)
		if len(changed) == 0 {
			newRevision = meta.Revision
			return nil
		}
		newRevision = meta.Revision + 1
		if err := store.ReplaceDraft(ctx, tx, templateID, newRevision, actor, s.now(), files); err != nil {
			return err
		}
		listed := changed
		if len(listed) > maxEventFiles {
			listed = listed[:maxEventFiles]
		}
		_, err = s.ev.Record(ctx, tx, events.Event{Type: "template.draft_saved", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"by": by, "files": listed, "changed": len(changed)}})
		return err
	})
	return newRevision, err
}

// changedPaths lists the paths added, removed or different, sorted.
func changedPaths(a, b map[string][]byte) []string {
	var out []string
	for p, ac := range a {
		if bc, ok := b[p]; !ok || !bytes.Equal(ac, bc) {
			out = append(out, p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ValidateDraft runs every check on the draft and records
// template.draft_validated. It changes nothing else.
func (s *Service) ValidateDraft(ctx context.Context, templateID int64, by, actor string) (finding.Report, error) {
	d, err := s.Draft(ctx, templateID)
	if err != nil {
		return finding.Report{}, err
	}
	report, err := s.validate(ctx, templateID, d.Files)
	if err != nil {
		return finding.Report{}, err
	}
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := s.ev.Record(ctx, tx, events.Event{Type: "template.draft_validated", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"by": by, "errors": len(report.Errors()), "warnings": len(report.Warnings())}})
		return err
	})
	return report, err
}

// validate checks files as the next version of template id.
func (s *Service) validate(ctx context.Context, id int64, files map[string][]byte) (finding.Report, error) {
	t, err := store.GetTemplate(ctx, s.d.R, id)
	if err != nil {
		return finding.Report{}, err
	}
	prev, err := previous(ctx, s.d.R, id)
	if err != nil {
		return finding.Report{}, err
	}
	return validate.Validate(ctx, validate.Input{Slug: t.Slug, Files: files, Previous: prev}), nil
}

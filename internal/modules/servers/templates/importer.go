package templates

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// ImportTarget says where an import goes: an existing template's draft
// (TemplateID), or a new template named Name with the slug Slug.
type ImportTarget struct {
	TemplateID  int64 // 0: a new template
	Name, Slug  string
	Description string
}

// ManifestName is the manifest's own name, to default a new template's name.
func ManifestName(files map[string][]byte) string {
	if m, _ := manifest.Parse(files[manifest.Name]); m != nil {
		return m.Name
	}
	return ""
}

// Import makes files the draft of a new template or replaces the draft of an
// existing one (the caller confirmed that), recording where they came from.
// The draft is validated like any other before it can be published. It
// returns the template's id.
func (s *Service) Import(ctx context.Context, into ImportTarget, files map[string][]byte, src Source, actor string) (int64, error) {
	if _, ok := files[manifest.Name]; !ok {
		return 0, ErrManifestNotFound
	}
	if err := checkImportSize(files); err != nil {
		return 0, err
	}
	if into.TemplateID == 0 {
		if fe := ValidateNew(into.Name, into.Slug); fe != nil {
			return 0, fe
		}
	}
	id := into.TemplateID
	err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
		at := s.now()
		basedOn := 0
		if id == 0 {
			var err error
			if id, err = store.InsertTemplate(ctx, tx, into.Slug, into.Name, into.Description, at); err != nil {
				return err
			}
			if _, err := s.ev.Record(ctx, tx, events.Event{Type: "template.created", Subject: subject(id), Actor: actor,
				Payload: map[string]any{"slug": into.Slug}}); err != nil {
				return err
			}
		} else {
			if _, err := store.GetTemplate(ctx, tx, id); err != nil {
				return err
			}
			var err error
			if basedOn, err = store.LatestVersion(ctx, tx, id); err != nil {
				return err
			}
		}
		if _, err := store.ResetDraft(ctx, tx, id, basedOn, src.json(), actor, at, files); err != nil {
			return err
		}
		_, err := s.ev.Record(ctx, tx, events.Event{Type: "template.draft_saved", Subject: subject(id), Actor: actor,
			Payload: map[string]any{"by": "admin", "files": len(files), "source": src.Kind}})
		return err
	})
	if err != nil {
		if into.TemplateID == 0 {
			id = 0
		}
		return id, err
	}
	return id, nil
}

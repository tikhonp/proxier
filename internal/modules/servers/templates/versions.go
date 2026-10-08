package templates

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// ErrSlugFrozen is returned when a rename changes the slug of a template
// that has a version.
var ErrSlugFrozen = errors.New("the slug can't change after the first version")

// Summary is a template as the list shows it.
type Summary struct {
	ID             int64
	Slug, Name     string
	Description    string
	DefaultVersion int
	Versions       int
	HasDraft       bool
	DraftBasedOn   int
	DraftSource    Source
	Archived       bool
	// ServersByVersion is version → lifecycle state → servers.
	ServersByVersion map[int]map[string]int
}

// Servers is the number of servers built from the template, any version and
// state.
func (s Summary) Servers() int {
	n := 0
	for _, by := range s.ServersByVersion {
		for _, c := range by {
			n += c
		}
	}
	return n
}

// List returns the templates by name; archived ones only when asked.
func (s *Service) List(ctx context.Context, showArchived bool) ([]Summary, error) {
	ts, err := store.ListTemplates(ctx, s.d.R)
	if err != nil {
		return nil, err
	}
	counts, err := store.VersionCounts(ctx, s.d.R)
	if err != nil {
		return nil, err
	}
	drafts, err := store.DraftMetas(ctx, s.d.R)
	if err != nil {
		return nil, err
	}
	servers, err := store.ServerCounts(ctx, s.d.R, 0)
	if err != nil {
		return nil, err
	}
	byTemplate := map[int64]map[int]map[string]int{}
	for _, c := range servers {
		if byTemplate[c.TemplateID] == nil {
			byTemplate[c.TemplateID] = map[int]map[string]int{}
		}
		if byTemplate[c.TemplateID][c.Version] == nil {
			byTemplate[c.TemplateID][c.Version] = map[string]int{}
		}
		byTemplate[c.TemplateID][c.Version][c.State] = c.N
	}
	var out []Summary
	for _, t := range ts {
		archived := !t.ArchivedAt.IsZero()
		if archived && !showArchived {
			continue
		}
		sum := Summary{ID: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description, DefaultVersion: t.DefaultVersion,
			Versions: counts[t.ID], Archived: archived, ServersByVersion: byTemplate[t.ID]}
		if d, ok := drafts[t.ID]; ok {
			sum.HasDraft, sum.DraftBasedOn, sum.DraftSource = true, d.BasedOn, parseSource(d.Source)
		}
		out = append(out, sum)
	}
	return out, nil
}

// ArchivedCount is how many templates are archived, for the filter chip.
func (s *Service) ArchivedCount(ctx context.Context) (int, error) {
	ts, err := store.ListTemplates(ctx, s.d.R)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range ts {
		if !t.ArchivedAt.IsZero() {
			n++
		}
	}
	return n, nil
}

// Info is a template's own row.
type Info struct {
	ID               int64
	Slug, Name       string
	Description      string
	DefaultVersion   int
	Archived         bool
	ArchivedAt       db.Time
	CreatedAt        db.Time
	Latest           int
	Servers          int // built from any version, any state
	ServersByVersion map[int]map[string]int
	DraftBasedOn     int
	DraftUpdatedAt   db.Time
	DraftUpdatedBy   string
	DraftSource      Source
	DraftRevision    int
	HasDraft         bool
}

// Get returns a template with what its page shows besides the versions.
func (s *Service) Get(ctx context.Context, id int64) (Info, error) {
	t, err := store.GetTemplate(ctx, s.d.R, id)
	if err != nil {
		return Info{}, err
	}
	latest, err := store.LatestVersion(ctx, s.d.R, id)
	if err != nil {
		return Info{}, err
	}
	counts, err := store.ServerCounts(ctx, s.d.R, id)
	if err != nil {
		return Info{}, err
	}
	info := Info{ID: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description, DefaultVersion: t.DefaultVersion,
		Archived: !t.ArchivedAt.IsZero(), ArchivedAt: t.ArchivedAt, CreatedAt: t.CreatedAt, Latest: latest,
		ServersByVersion: map[int]map[string]int{}}
	for _, c := range counts {
		if info.ServersByVersion[c.Version] == nil {
			info.ServersByVersion[c.Version] = map[string]int{}
		}
		info.ServersByVersion[c.Version][c.State] = c.N
		info.Servers += c.N
	}
	switch d, err := store.GetDraft(ctx, s.d.R, id); {
	case err == nil:
		info.HasDraft, info.DraftBasedOn, info.DraftRevision = true, d.BasedOn, d.Revision
		info.DraftUpdatedAt, info.DraftUpdatedBy, info.DraftSource = d.UpdatedAt, d.UpdatedBy, parseSource(d.Source)
	case !errors.Is(err, store.ErrNotFound):
		return Info{}, err
	}
	return info, nil
}

// VersionInfo is a version as the versions table shows it.
type VersionInfo struct {
	Number      int
	Notes       string
	PublishedAt db.Time
	PublishedBy string
	Warnings    int
	Source      Source
	Default     bool
	Servers     map[string]int // lifecycle state → count
}

// Versions lists a template's versions, newest first.
func (s *Service) Versions(ctx context.Context, templateID int64) ([]VersionInfo, error) {
	t, err := store.GetTemplate(ctx, s.d.R, templateID)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListVersions(ctx, s.d.R, templateID)
	if err != nil {
		return nil, err
	}
	counts, err := store.ServerCounts(ctx, s.d.R, templateID)
	if err != nil {
		return nil, err
	}
	servers := map[int]map[string]int{}
	for _, c := range counts {
		if servers[c.Version] == nil {
			servers[c.Version] = map[string]int{}
		}
		servers[c.Version][c.State] = c.N
	}
	out := make([]VersionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, VersionInfo{Number: r.Number, Notes: r.Notes, PublishedAt: r.PublishedAt, PublishedBy: r.PublishedBy,
			Warnings: len(decodeWarnings(r.Warnings)), Source: parseSource(r.Source), Default: r.Number == t.DefaultVersion, Servers: servers[r.Number]})
	}
	return out, nil
}

// MakeDefault makes version the one new servers are built from. The version
// that already is the default records nothing.
func (s *Service) MakeDefault(ctx context.Context, templateID int64, version int, actor string) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		t, err := store.GetTemplate(ctx, tx, templateID)
		if err != nil {
			return err
		}
		if _, err := store.GetVersion(ctx, tx, templateID, version); err != nil {
			return err
		}
		if t.DefaultVersion == version {
			return nil
		}
		if err := store.SetDefaultVersion(ctx, tx, templateID, version); err != nil {
			return err
		}
		_, err = s.ev.Record(ctx, tx, events.Event{Type: "template.default_version_changed", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"from": t.DefaultVersion, "to": version}})
		return err
	})
}

// DraftFromVersion makes a version's files the draft (replacing any draft),
// based on that version. Publishing it makes the next number, so history only
// grows. It returns the draft's new revision.
func (s *Service) DraftFromVersion(ctx context.Context, templateID int64, version int, actor string) (revision int, err error) {
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.GetTemplate(ctx, tx, templateID); err != nil {
			return err
		}
		row, err := store.GetVersion(ctx, tx, templateID, version)
		if err != nil {
			return err
		}
		files, err := store.VersionFiles(ctx, tx, row.ID)
		if err != nil {
			return err
		}
		src := Source{Kind: "version", Version: version}
		if revision, err = store.ResetDraft(ctx, tx, templateID, version, src.json(), actor, s.now(), files); err != nil {
			return err
		}
		_, err = s.ev.Record(ctx, tx, events.Event{Type: "template.draft_saved", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"by": "admin", "files": len(files), "from_version": version}})
		return err
	})
	return revision, err
}

// DiscardDraft deletes the draft. ErrNoDraft when there is none.
func (s *Service) DiscardDraft(ctx context.Context, templateID int64, actor string) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		meta, err := store.GetDraft(ctx, tx, templateID)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNoDraft
		}
		if err != nil {
			return err
		}
		if err := store.DeleteDraft(ctx, tx, templateID); err != nil {
			return err
		}
		if _, err = s.ev.Record(ctx, tx, events.Event{Type: "template.draft_discarded", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"based_on": meta.BasedOn, "revision": meta.Revision}}); err != nil {
			return err
		}
		return s.draftEnded(ctx, tx, templateID, "discarded", actor)
	})
}

// Archive hides a template from the new-server form (archived true) or brings
// it back. A template already in that state records nothing.
func (s *Service) Archive(ctx context.Context, templateID int64, archived bool, actor string) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		t, err := store.GetTemplate(ctx, tx, templateID)
		if err != nil {
			return err
		}
		if archived == !t.ArchivedAt.IsZero() {
			return nil
		}
		typ := "template.unarchived"
		var at *db.Time
		if archived {
			typ = "template.archived"
			n := s.now()
			at = &n
		}
		if err := store.SetArchived(ctx, tx, templateID, at); err != nil {
			return err
		}
		_, err = s.ev.Record(ctx, tx, events.Event{Type: typ, Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"slug": t.Slug}})
		return err
	})
}

// Delete removes a template that never built a server, with its draft and
// versions. A template any server was built from (retired ones included) is
// ErrInUse: archive it instead.
func (s *Service) Delete(ctx context.Context, templateID int64, actor string) error {
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		t, err := store.GetTemplate(ctx, tx, templateID)
		if err != nil {
			return err
		}
		used, err := store.TemplateUsed(ctx, tx, templateID)
		if err != nil {
			return err
		}
		if used {
			return ErrInUse
		}
		if err := store.DeleteTemplate(ctx, tx, templateID); err != nil {
			return err
		}
		_, err = s.ev.Record(ctx, tx, events.Event{Type: "template.deleted", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"slug": t.Slug, "name": t.Name}})
		return err
	})
}

// Rename changes the name and description, and the slug while the template
// has no version. Messages of a refusal are i18n keys (store.FieldErrors).
// Nothing changes, nothing is recorded, when the values are the stored ones.
func (s *Service) Rename(ctx context.Context, templateID int64, name, slug, description, actor string) error {
	if fe := ValidateNew(name, slug); fe != nil {
		return fe
	}
	return s.d.Write(ctx, func(tx *sqlx.Tx) error {
		t, err := store.GetTemplate(ctx, tx, templateID)
		if err != nil {
			return err
		}
		if name == t.Name && slug == t.Slug && description == t.Description {
			return nil
		}
		var newSlug *string
		if slug != t.Slug {
			latest, err := store.LatestVersion(ctx, tx, templateID)
			if err != nil {
				return err
			}
			if latest > 0 {
				return ErrSlugFrozen
			}
			newSlug = &slug
		}
		if err := store.UpdateTemplate(ctx, tx, templateID, name, description, newSlug); err != nil {
			return err
		}
		changed := []string{}
		if name != t.Name {
			changed = append(changed, "name")
		}
		if slug != t.Slug {
			changed = append(changed, "slug")
		}
		if description != t.Description {
			changed = append(changed, "description")
		}
		sort.Strings(changed)
		_, err = s.ev.Record(ctx, tx, events.Event{Type: "template.changed", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"fields": strings.Join(changed, ", ")}})
		return err
	})
}

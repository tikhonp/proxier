// Package templates keeps the module's templates: drafts, publishing and
// versions (docs/processes/servers/template-authoring.md). Validation is
// pure Go (servers/validate) and runs before the transaction that writes, so
// nothing slow happens while the write connection is held.
package templates

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

var (
	ErrDraftChanged = errors.New("the draft changed since you opened it")
	ErrNoDraft      = errors.New("no draft")
	ErrSlugTaken    = store.ErrSlugTaken
	ErrInUse        = errors.New("a version of this template built a server")
	ErrNotFound     = store.ErrNotFound
)

// ValidationError is returned by Publish when validation found errors.
type ValidationError struct{ Report finding.Report }

func (e *ValidationError) Error() string {
	return "the template has errors: " + firstMessage(e.Report.Errors())
}

// WarningsError is returned by Publish when validation found only warnings
// and the caller did not confirm them.
type WarningsError struct{ Report finding.Report }

func (e *WarningsError) Error() string {
	return "the template has warnings that need confirming: " + firstMessage(e.Report.Warnings())
}

func firstMessage(fs []finding.Finding) string {
	if len(fs) == 0 {
		return ""
	}
	return fs[0].Message
}

// Source says where a draft came from.
type Source struct {
	Kind    string `json:"kind,omitempty"` // "zip" "git" "version"
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Path    string `json:"path,omitempty"`
	Version int    `json:"version,omitempty"`
}

func (s Source) json() string {
	b, _ := json.Marshal(s)
	return string(b)
}

func parseSource(raw string) Source {
	var s Source
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

// Draft is the working copy of a template.
type Draft struct {
	TemplateID int64
	BasedOn    int // 0 = none
	Revision   int
	Files      map[string][]byte
	Source     Source
	UpdatedAt  db.Time
	UpdatedBy  string
}

// Version is a published version.
type Version struct {
	TemplateID  int64
	Number      int
	Notes       string
	Files       map[string][]byte
	Warnings    []finding.Finding
	Source      Source
	PublishedAt db.Time
	PublishedBy string
}

// Service is the template lifecycle.
type Service struct {
	Now func() time.Time
	// Git fetches import sources; the zero value is production's. Tests point
	// it at an httptest git server.
	Git GitFetcher

	d   *db.DB
	ev  *events.Catalog
	log *slog.Logger
}

// New returns a service.
func New(d *db.DB, ev *events.Catalog, log *slog.Logger) *Service {
	return &Service{Now: time.Now, d: d, ev: ev, log: log}
}

// FetchGit imports a path of a public repository, with the service's fetcher.
func (s *Service) FetchGit(ctx context.Context, src GitSource) (map[string][]byte, string, error) {
	return s.Git.Fetch(ctx, src)
}

func (s *Service) now() db.Time { return db.At(s.Now()) }

func subject(id int64) events.Subject {
	return events.Subject{Type: "template", ID: itoa(id)}
}

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// ValidateNew checks a new template's name and slug. Messages are i18n keys.
func ValidateNew(name, slug string) store.FieldErrors {
	fe := store.FieldErrors{}
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		fe["name"] = "templates.err.name"
	}
	if !slugPattern.MatchString(slug) {
		fe["slug"] = "templates.err.slug"
	}
	if len(fe) == 0 {
		return nil
	}
	return fe
}

// Create adds a template with a draft holding a manifest skeleton, and
// records template.created.
func (s *Service) Create(ctx context.Context, name, slug, description, actor string) (id int64, err error) {
	if fe := ValidateNew(name, slug); fe != nil {
		return 0, fe
	}
	err = s.d.Write(ctx, func(tx *sqlx.Tx) error {
		at := s.now()
		if id, err = store.InsertTemplate(ctx, tx, slug, name, description, at); err != nil {
			return err
		}
		if err = store.InsertDraft(ctx, tx, id, 0, "{}", actor, at, Skeleton(name, slug, description)); err != nil {
			return err
		}
		_, err = s.ev.Record(ctx, tx, events.Event{Type: "template.created", Subject: subject(id), Actor: actor,
			Payload: map[string]any{"slug": slug}})
		return err
	})
	return id, err
}

// Draft returns a template's draft, ErrNoDraft when it has none.
func (s *Service) Draft(ctx context.Context, templateID int64) (Draft, error) {
	return readDraft(ctx, s.d.R, templateID)
}

func readDraft(ctx context.Context, q sqlx.QueryerContext, id int64) (Draft, error) {
	m, err := store.GetDraft(ctx, q, id)
	if errors.Is(err, store.ErrNotFound) {
		return Draft{}, ErrNoDraft
	}
	if err != nil {
		return Draft{}, err
	}
	files, err := store.DraftFiles(ctx, q, id)
	if err != nil {
		return Draft{}, err
	}
	return Draft{TemplateID: id, BasedOn: m.BasedOn, Revision: m.Revision, Files: files, Source: parseSource(m.Source),
		UpdatedAt: m.UpdatedAt, UpdatedBy: m.UpdatedBy}, nil
}

// EditDraft returns the template's draft, creating one from its latest
// version when it has none.
func (s *Service) EditDraft(ctx context.Context, templateID int64, actor string) (Draft, error) {
	var out Draft
	err := s.d.Write(ctx, func(tx *sqlx.Tx) error {
		t, err := store.GetTemplate(ctx, tx, templateID)
		if err != nil {
			return err
		}
		d, err := readDraft(ctx, tx, templateID)
		if err == nil {
			out = d
			return nil
		}
		if !errors.Is(err, ErrNoDraft) {
			return err
		}
		latest, err := store.LatestVersion(ctx, tx, templateID)
		if err != nil {
			return err
		}
		// A template that never published and lost its draft starts over
		// from the skeleton.
		files := Skeleton(t.Name, t.Slug, t.Description)
		src := Source{}
		if latest > 0 {
			v, err := store.GetVersion(ctx, tx, templateID, latest)
			if err != nil {
				return err
			}
			if files, err = store.VersionFiles(ctx, tx, v.ID); err != nil {
				return err
			}
			src = Source{Kind: "version", Version: latest}
		}
		if err := store.InsertDraft(ctx, tx, templateID, latest, src.json(), actor, s.now(), files); err != nil {
			return err
		}
		if _, err := s.ev.Record(ctx, tx, events.Event{Type: "template.draft_saved", Subject: subject(templateID), Actor: actor,
			Payload: map[string]any{"by": "admin", "files": len(files), "from_version": latest}}); err != nil {
			return err
		}
		out, err = readDraft(ctx, tx, templateID)
		return err
	})
	return out, err
}

// Version returns a published version with its files.
func (s *Service) Version(ctx context.Context, templateID int64, number int) (Version, error) {
	row, err := store.GetVersion(ctx, s.d.R, templateID, number)
	if err != nil {
		return Version{}, err
	}
	files, err := store.VersionFiles(ctx, s.d.R, row.ID)
	if err != nil {
		return Version{}, err
	}
	return Version{TemplateID: templateID, Number: number, Notes: row.Notes, Files: files, Source: parseSource(row.Source),
		Warnings: decodeWarnings(row.Warnings), PublishedAt: row.PublishedAt, PublishedBy: row.PublishedBy}, nil
}

func decodeWarnings(raw string) []finding.Finding {
	var w []finding.Finding
	_ = json.Unmarshal([]byte(raw), &w)
	return w
}

// previous is the manifest of the template's latest version, nil for none, for
// the dropped-endpoint-key warning.
func previous(ctx context.Context, q sqlx.QueryerContext, id int64) (*manifest.Manifest, error) {
	latest, err := store.LatestVersion(ctx, q, id)
	if err != nil || latest == 0 {
		return nil, err
	}
	row, err := store.GetVersion(ctx, q, id, latest)
	if err != nil {
		return nil, err
	}
	files, err := store.VersionFiles(ctx, q, row.ID)
	if err != nil {
		return nil, err
	}
	m, _ := manifest.Parse(files[manifest.Name])
	return m, nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Slugify makes a slug from a name: lower-case letters, digits and single
// dashes, starting with a letter, at most 40 characters. A name with nothing
// usable gives "".
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s != "" && (s[0] < 'a' || s[0] > 'z') {
		s = "t-" + s
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if len(s) < 2 {
		return ""
	}
	return s
}

// Package scripts holds router scripts: their drafts, publishing, versions,
// the current version, diffs, archive and delete (docs/processes/
// router-scripts/script-versions.md). A version's body is stored byte for
// byte and parsed on demand.
package scripts

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

var (
	ErrNotFound       = errors.New("scripts: no such router script")
	ErrNoVersion      = errors.New("scripts: no such version")
	ErrNoDraft        = errors.New("scripts: the script has no draft")
	ErrDraftChanged   = errors.New("scripts: the draft changed meanwhile")
	ErrHasGenerations = errors.New("scripts: generations were made from the script")
	ErrArchived       = errors.New("scripts: the script is archived")
)

// Limits of a script's fields.
const (
	MaxName        = 60
	MaxDescription = 500
	MaxNotes       = 500
	MaxBody        = 512 << 10
)

var slugShape = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// PublishError: errors (Blocked), or warnings that weren't confirmed.
type PublishError struct {
	Findings []params.Finding
	Blocked  bool
}

func (e *PublishError) Error() string {
	if e.Blocked {
		return "scripts: the draft has errors"
	}
	return "scripts: the draft's warnings weren't confirmed"
}

// Script is a router script.
type Script struct {
	ID                      int64
	Name, Slug, Description string
	Current                 int // 0 before the first version
	Archived                bool
	CreatedAt               time.Time
}

// Summary is a row of the list.
type Summary struct {
	Script
	PublishedAt time.Time // the current version's
	Generations int
	Draft       bool
	DraftParams int // parameters the draft has
	DraftErrors int
}

// Draft is a script's draft.
type Draft struct {
	Body      string
	BasedOn   int // 0: none
	Revision  int
	UpdatedAt time.Time
	UpdatedBy string
}

// Version is a published version.
type Version struct {
	Number      int
	Body        string // empty in Versions
	SHA256      string
	Warnings    []params.Finding
	Notes       string
	PublishedAt time.Time
	PublishedBy string
	Generations int
	Current     bool
}

// New is what a new script is made of.
type New struct{ Name, Slug, Description, Body string }

// Details are a script's editable fields.
type Details struct{ Name, Slug, Description string }

// Deps are what the service uses.
type Deps struct {
	DB     *db.DB
	Events *events.Catalog
	Now    func() time.Time
	Log    *slog.Logger
}

// Service manages router scripts.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

// Subject is a script's event subject.
func Subject(id int64) events.Subject {
	return events.Subject{Type: "router_script", ID: strconv.FormatInt(id, 10)}
}

func (s *Service) record(ctx context.Context, tx *sqlx.Tx, typ string, id int64, actor string, payload map[string]any) error {
	_, err := s.d.Events.Record(ctx, tx, events.Event{Time: db.At(s.d.Now()), Type: typ, Subject: Subject(id), Actor: actor, Payload: payload})
	return err
}

var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

// SuggestSlug makes a slug from a name: lower case, a trailing ".rsc"
// dropped, every run of other characters a "-" ("fresh-router.rsc" →
// "fresh-router").
func SuggestSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.TrimSuffix(s, ".rsc")
	s = strings.Trim(slugRun.ReplaceAllString(s, "-"), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

func toScript(r store.Script) Script {
	return Script{ID: r.ID, Name: r.Name, Slug: r.Slug, Description: r.Description, Current: r.Current, Archived: r.Archived, CreatedAt: r.CreatedAt.Time}
}

// checkDetails cleans a script's fields and puts their errors into fe.
func checkDetails(ctx context.Context, q sqlx.QueryerContext, d Details, except int64, fe store.FieldErrors) (Details, error) {
	d.Name = strings.TrimSpace(d.Name)
	d.Slug = strings.TrimSpace(d.Slug)
	d.Description = strings.TrimSpace(d.Description)
	if d.Slug == "" {
		d.Slug = SuggestSlug(d.Name)
	}
	if n := utf8.RuneCountInString(d.Name); n < 1 || n > MaxName {
		fe["name"] = "scripts.err.name"
	} else if taken, err := store.Taken(ctx, q, "name", d.Name, except); err != nil {
		return d, err
	} else if taken {
		fe["name"] = "scripts.err.name_taken"
	}
	if !slugShape.MatchString(d.Slug) {
		fe["slug"] = "scripts.err.slug"
	} else if taken, err := store.Taken(ctx, q, "slug", d.Slug, except); err != nil {
		return d, err
	} else if taken {
		fe["slug"] = "scripts.err.slug_taken"
	}
	if utf8.RuneCountInString(d.Description) > MaxDescription {
		fe["description"] = "scripts.err.description"
	}
	return d, nil
}

// checkBody puts a body's error into fe under "body".
func checkBody(body string, fe store.FieldErrors) {
	switch {
	case body == "":
		fe["body"] = "scripts.err.body_empty"
	case len(body) > MaxBody:
		fe["body"] = "scripts.err.body_large"
	case !utf8.ValidString(body) || strings.ContainsRune(body, 0):
		fe["body"] = "scripts.err.body_binary"
	}
}

// Create makes a script and its draft (based on no version, revision 1).
// Errors are store.FieldErrors; nothing is created then.
func (s *Service) Create(ctx context.Context, n New, actor string) (int64, error) {
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		fe := store.FieldErrors{}
		d, err := checkDetails(ctx, tx, Details{Name: n.Name, Slug: n.Slug, Description: n.Description}, 0, fe)
		if err != nil {
			return err
		}
		checkBody(n.Body, fe)
		if len(fe) > 0 {
			return fe
		}
		now := db.At(s.d.Now())
		if id, err = store.InsertScript(ctx, tx, store.Script{Name: d.Name, Slug: d.Slug, Description: d.Description, CreatedAt: now}); err != nil {
			return err
		}
		if err := store.InsertDraft(ctx, tx, store.Draft{ScriptID: id, Body: n.Body, Revision: 1, UpdatedAt: now, UpdatedBy: actor}); err != nil {
			return err
		}
		return s.record(ctx, tx, "routerscript.created", id, actor, map[string]any{"name": d.Name})
	})
	return id, err
}

// Get reads a script.
func (s *Service) Get(ctx context.Context, id int64) (Script, error) {
	r, err := store.GetScript(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Script{}, ErrNotFound
	}
	return toScript(r), err
}

// List reads the scripts that aren't archived, or with archived only the
// archived ones, by name.
func (s *Service) List(ctx context.Context, archived bool) ([]Summary, error) {
	rows, err := store.Scripts(ctx, s.d.DB.R, archived)
	if err != nil {
		return nil, err
	}
	gens, err := store.GenerationCounts(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	drafts, err := store.Drafts(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	published, err := store.PublishedAt(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		sum := Summary{Script: toScript(r), Generations: gens[r.ID], PublishedAt: published[r.ID].Time}
		if d, ok := drafts[r.ID]; ok {
			p := params.Parse([]byte(d.Body))
			sum.Draft, sum.DraftParams, sum.DraftErrors = true, len(p.Params), len(p.Errors())
		}
		out = append(out, sum)
	}
	return out, nil
}

// ArchivedCount counts the archived scripts.
func (s *Service) ArchivedCount(ctx context.Context) (int, error) {
	return store.ArchivedCount(ctx, s.d.DB.R)
}

// Generations counts the generations made from a script.
func (s *Service) Generations(ctx context.Context, id int64) (int, error) {
	return store.Generations(ctx, s.d.DB.R, id)
}

// Edit changes a script's name, slug and description; the slug is fixed once
// a version exists. It records routerscript.changed{fields} when something
// changed.
func (s *Service) Edit(ctx context.Context, id int64, d Details, actor string) (bool, error) {
	changed := false
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetScript(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		fe := store.FieldErrors{}
		if cur.Current > 0 && strings.TrimSpace(d.Slug) == "" {
			d.Slug = cur.Slug
		}
		d, err = checkDetails(ctx, tx, d, id, fe)
		if err != nil {
			return err
		}
		if cur.Current > 0 && d.Slug != cur.Slug && fe["slug"] == "" {
			fe["slug"] = "scripts.err.slug_fixed"
		}
		if len(fe) > 0 {
			return fe
		}
		var fields []string
		if d.Name != cur.Name {
			fields = append(fields, "name")
		}
		if d.Slug != cur.Slug {
			fields = append(fields, "slug")
		}
		if d.Description != cur.Description {
			fields = append(fields, "description")
		}
		if len(fields) == 0 {
			return nil
		}
		changed = true
		if err := store.UpdateScript(ctx, tx, id, d.Name, d.Slug, d.Description); err != nil {
			return err
		}
		return s.record(ctx, tx, "routerscript.changed", id, actor, map[string]any{"fields": strings.Join(fields, ", ")})
	})
	return changed, err
}

// Archive archives or unarchives a script; the same state changes nothing.
func (s *Service) Archive(ctx context.Context, id int64, archived bool, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetScript(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil || cur.Archived == archived {
			return err
		}
		if err := store.SetArchived(ctx, tx, id, archived); err != nil {
			return err
		}
		return s.record(ctx, tx, "routerscript.archived", id, actor, map[string]any{"archived": archived})
	})
}

// Delete deletes a script with its versions and draft, only while no
// generation was made from it (ErrHasGenerations).
func (s *Service) Delete(ctx context.Context, id int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetScript(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		n, err := store.Generations(ctx, tx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrHasGenerations
		}
		if err := store.DeleteScript(ctx, tx, id); err != nil {
			return err
		}
		return s.record(ctx, tx, "routerscript.deleted", id, actor, map[string]any{"name": cur.Name})
	})
}

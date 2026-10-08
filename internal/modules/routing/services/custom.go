package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/change"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

var origins = []string{"", "discovery", "import", "router"}

func nowAt(s *Service) db.Time { return db.At(s.d.Now()) }

// fields checks a custom service's name, tag and description against the
// other services (except id); it returns the tag to use.
func (s *Service) fields(ctx context.Context, q sqlx.QueryerContext, id int64, name, tag, description string, fe store.FieldErrors) (string, error) {
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxName {
		fe["name"] = "services.err.name"
	} else if taken, err := store.CustomNameTaken(ctx, q, name, id); err != nil {
		return "", err
	} else if taken {
		fe["name"] = "services.err.name_taken"
	}
	if tag == "" {
		tag = selector.Slug(name)
	}
	switch {
	case tag == "":
		fe["tag"] = "services.err.tag_empty"
	case selector.ValidTag(tag) != nil:
		fe["tag"] = "services.err.tag"
	default:
		taken, err := store.TagTaken(ctx, q, tag, id)
		if err != nil {
			return "", err
		}
		if taken {
			fe["tag"] = "services.err.tag_taken"
		}
	}
	if utf8.RuneCountInString(description) > MaxDescription {
		fe["description"] = "services.err.description"
	}
	return tag, nil
}

// CreateCustom makes a custom service with no domains: its first snapshot is
// empty (it installs nothing until saved with names).
func (s *Service) CreateCustom(ctx context.Context, c Custom, actor string) (int64, error) {
	name, tag, desc := strings.TrimSpace(c.Name), strings.ToLower(strings.TrimSpace(c.Tag)), strings.TrimSpace(c.Description)
	if !slices.Contains(origins, c.Origin) {
		return 0, fmt.Errorf("services: unknown origin %q", c.Origin)
	}
	var id int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		fe := store.FieldErrors{}
		tag, err := s.fields(ctx, tx, 0, name, tag, desc, fe)
		if err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		id, err = store.InsertService(ctx, tx, store.Service{
			Tag: tag, Source: string(selector.Custom), Name: name, Description: desc, Origin: c.Origin, CreatedAt: nowAt(s),
		})
		if err != nil {
			return err
		}
		snap, err := s.newSnapshot(id, "", "", "", snapshot.New(nil, nil, nil), nil)
		if err != nil {
			return err
		}
		if _, err := store.InsertSnapshot(ctx, tx, snap); err != nil {
			return err
		}
		return s.record(ctx, tx, "routing.service_added", id, actor, map[string]any{
			"selector": "", "tag": tag, "source": string(selector.Custom), "origin": c.Origin,
		})
	})
	return id, err
}

// checkRows normalises the editor's rows; a row that isn't a domain, or has
// too long a note, is a field error "row.<n>" (from 1).
func checkRows(in []DomainRow, fe store.FieldErrors) []DomainRow {
	var rows []DomainRow
	for i, r := range in {
		if strings.TrimSpace(r.Domain) == "" {
			continue
		}
		key := fmt.Sprintf("row.%d", i+1)
		n, err := domain.Normalise(r.Domain)
		if err != nil {
			fe[key] = reason(err)
			continue
		}
		r.Note = strings.TrimSpace(r.Note)
		if utf8.RuneCountInString(r.Note) > MaxNote {
			fe[key] = "services.err.note"
			continue
		}
		rows = append(rows, DomainRow{Domain: n.Domain, Exact: r.Exact || n.Exact, Note: r.Note})
	}
	if len(rows) > MaxRows {
		fe["rows"] = "services.err.too_many"
	}
	return rows
}

func rowSet(rows []DomainRow) snapshot.Set {
	var suffix, exact []string
	for _, r := range rows {
		if r.Exact {
			exact = append(exact, r.Domain)
		} else {
			suffix = append(suffix, r.Domain)
		}
	}
	return snapshot.New(suffix, exact, nil)
}

func storedRows(rows []DomainRow) []store.CustomDomain {
	out := make([]store.CustomDomain, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.CustomDomain{Domain: r.Domain, Exact: r.Exact, Note: r.Note})
	}
	slices.SortFunc(out, func(a, b store.CustomDomain) int { return strings.Compare(a.Domain, b.Domain) })
	return out
}

// SaveCustom saves the domain editor: everything is checked first, then one
// transaction writes the rows, a new snapshot when the names changed and a
// renamed tag. Nothing changed records nothing.
func (s *Service) SaveCustom(ctx context.Context, id int64, e Edit, actor string) (Saved, error) {
	fe := store.FieldErrors{}
	rows := checkRows(e.Rows, fe)
	var rep Report
	rows, rep.Merged = merge(rows)
	for _, r := range rows {
		if u := domain.Unicode(r.Domain); u != "" {
			rep.Punycode = append(rep.Punycode, domain.Name{Domain: r.Domain, Exact: r.Exact, Unicode: u})
		}
	}
	name, tag, desc := strings.TrimSpace(e.Name), strings.ToLower(strings.TrimSpace(e.Tag)), strings.TrimSpace(e.Description)
	set := rowSet(rows)
	saved := Saved{Report: rep}
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		cur, err := store.GetService(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Source != string(selector.Custom) {
			return ErrNotCustom
		}
		if tag, err = s.fields(ctx, tx, id, name, tag, desc, fe); err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		if s.d.Check != nil {
			if err := s.d.Check(ctx, tx, id, set); err != nil {
				return err
			}
		}
		oldRows, err := store.CustomDomains(ctx, tx, id)
		if err != nil {
			return err
		}
		old, hash, err := acceptedSet(ctx, tx, id)
		if err != nil {
			return err
		}
		var changes []string
		payload := map[string]any{}
		if name != cur.Name {
			changes = append(changes, "name")
		}
		if tag != cur.Tag {
			changes = append(changes, "tag")
			payload["from"], payload["to"] = cur.Tag, tag
		}
		if desc != cur.Description {
			changes = append(changes, "description")
		}
		newRows := storedRows(rows)
		if !slices.Equal(oldRows, newRows) {
			changes = append(changes, "domains")
			d := snapshot.Compare(old, set)
			payload["added"], payload["removed"] = d.Added(), d.Removed()
			if err := store.ReplaceCustomDomains(ctx, tx, id, newRows); err != nil {
				return err
			}
		}
		if len(changes) == 0 {
			return nil
		}
		saved.Changed = true
		if err := store.SetCustomFields(ctx, tx, id, name, tag, desc); err != nil {
			return err
		}
		snapChanged := set.Hash() != hash
		if snapChanged {
			snap, err := s.newSnapshot(id, "", "", "", set, &old)
			if err != nil {
				return err
			}
			if _, err := store.Accept(ctx, tx, snap); err != nil {
				return err
			}
		}
		if snapChanged || tag != cur.Tag {
			why := tag + " changed"
			if tag != cur.Tag {
				why = cur.Tag + " renamed to " + tag
			}
			if err := s.d.Marker.Mark(ctx, tx, change.Change{Services: []int64{id}, Actor: actor, Why: why}); err != nil {
				return err
			}
		}
		payload["changes"] = strings.Join(changes, ", ")
		return s.record(ctx, tx, "routing.service_updated", id, actor, payload)
	})
	if err != nil {
		return Saved{}, err
	}
	return saved, nil
}

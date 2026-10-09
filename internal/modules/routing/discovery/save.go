package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// picked keeps the picks the run offers: groups' registrable domains and
// tickable hosts. Anything else is dropped.
func (s *Service) picked(ctx context.Context, id int64, p Pick) (Run, snapshot.Set, error) {
	run, err := s.Get(ctx, id)
	if err != nil {
		return run, snapshot.Set{}, err
	}
	if run.Active() {
		return run, snapshot.Set{}, ErrNotDone
	}
	var suffix, exact []string
	for _, g := range run.Groups {
		if slices.Contains(p.Suffix, g.Registrable) {
			suffix = append(suffix, g.Registrable)
		}
		for _, h := range g.Hosts {
			if h.Tickable() && slices.Contains(p.Exact, h.Host) {
				exact = append(exact, h.Host)
			}
		}
	}
	if len(suffix)+len(exact) == 0 {
		return run, snapshot.Set{}, ErrNoPicks
	}
	return run, snapshot.New(suffix, exact, nil), nil
}

// DefaultName is what a new service from the run is called: the page title
// cut to the longest name, or the registrable domain.
func DefaultName(run Run) string {
	name := strings.Join(strings.Fields(run.Title), " ")
	if name == "" {
		return run.Registrable
	}
	if utf8.RuneCountInString(name) > services.MaxName {
		name = strings.TrimSpace(string([]rune(name)[:services.MaxName]))
	}
	return name
}

// Create makes a custom service (origin discovery) of the picks and adds it
// to the lists, in one transaction past the server-hostname guard (a
// refusal is a *lists.GuardError, recorded after the rollback, nothing
// created). Field errors are the services' (name, tag).
func (s *Service) Create(ctx context.Context, id int64, c services.Custom, p Pick, listIDs []int64, actor string) (int64, error) {
	_, set, err := s.picked(ctx, id, p)
	if err != nil {
		return 0, err
	}
	c.Origin = "discovery"
	tag := strings.ToLower(strings.TrimSpace(c.Tag))
	if tag == "" {
		tag = selector.Slug(c.Name)
	}
	if err := s.d.Lists.CheckSet(ctx, tag, set, listIDs); err != nil {
		s.d.Lists.Refused(ctx, err, actor)
		return 0, err
	}
	var sid int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		if sid, err = s.d.Services.CreateCustomTx(ctx, tx, c, set, actor); err != nil {
			return err
		}
		for _, l := range listIDs {
			if err := s.d.Lists.AddTx(ctx, tx, l, []int64{sid}, actor); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.d.Lists.Refused(ctx, err, actor)
		return 0, err
	}
	return sid, nil
}

// AddTo merges the picks into an existing custom service through the
// editor's save: its normalisation, absorption and guard.
func (s *Service) AddTo(ctx context.Context, id, serviceID int64, p Pick, actor string) error {
	_, set, err := s.picked(ctx, id, p)
	if err != nil {
		return err
	}
	it, err := s.d.Services.Get(ctx, serviceID)
	if err != nil {
		return err
	}
	if it.Source != selector.Custom {
		return ErrNotCustom
	}
	rows, err := s.d.Services.CustomRows(ctx, serviceID)
	if err != nil {
		return err
	}
	for _, n := range set.Suffix {
		rows = append(rows, services.DomainRow{Domain: n, Note: "discovery"})
	}
	for _, n := range set.Exact {
		rows = append(rows, services.DomainRow{Domain: n, Exact: true, Note: "discovery"})
	}
	_, err = s.d.Services.SaveCustom(ctx, serviceID, services.Edit{Name: it.Name, Tag: it.Tag, Description: it.Description, Rows: rows}, actor)
	return err
}

var shotName = regexp.MustCompile(`^[0-9]{1,2}-[0-9]{1,2}\.jpg$`)

// Screenshot is the path of a run's screenshot; ErrNotFound for a name the
// run didn't write.
func (s *Service) Screenshot(id int64, file string) (string, error) {
	if !shotName.MatchString(file) {
		return "", ErrNotFound
	}
	p := filepath.Join(s.runDir(id), file)
	if _, err := os.Stat(p); err != nil {
		return "", ErrNotFound
	}
	return p, nil
}

// Prune deletes the runs created more than Retention ago, each directory
// before its row, and directories no run owns.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	ids, err := store.RunsBefore(ctx, s.d.DB.R, dbAt(s.d.Now().Add(-Retention)))
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := os.RemoveAll(s.runDir(id)); err != nil {
			return 0, err
		}
		if err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.DeleteRun(ctx, tx, id) }); err != nil {
			return 0, err
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.d.DataDir, "discovery"))
	if errors.Is(err, os.ErrNotExist) {
		return int64(len(ids)), nil
	}
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		id, perr := parseID(e.Name())
		if perr != nil {
			continue
		}
		ok, err := store.RunExists(ctx, s.d.DB.R, id)
		if err != nil {
			return 0, err
		}
		if !ok {
			if err := os.RemoveAll(filepath.Join(s.d.DataDir, "discovery", e.Name())); err != nil {
				return 0, err
			}
		}
	}
	return int64(len(ids)), nil
}

// Package catalog keeps a local copy of what the upstream sources offer (v2fly
// lists with a reverse index, the iplist portals' sites and groups with their
// domains), refreshes it daily as a new generation per source, searches it
// instantly and looks names up in it (docs/processes/routing/catalog-search.md).
package catalog

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Sources are the catalog's sources, in their fixed order.
var Sources = []string{"v2fly", "iplist:main", "iplist:beta", "iplist:russia"}

// Entry is one thing search can find.
type Entry struct {
	Source  string // v2fly, iplist:main, iplist:beta, iplist:russia
	Kind    string // list, site, group
	Name    string
	Group   string // a site's group
	Sites   int    // a group's sites
	Domains int
}

// Result is a search hit.
type Result struct {
	Entry
	Selector   string // as it would be written
	Portal     string // iplist
	Selectable bool   // false: a group shadowed by a site of the same name
	Service    *services.Item
	Lists      []string
	Age        time.Duration // of its source's catalog
}

// Query is a search.
type Query struct {
	Q, Source, Kind, Portal string
	Limit                   int
}

// SourceStatus is a source's catalog.
type SourceStatus struct {
	Source       string
	RefreshedAt  time.Time // zero: never
	Entries      int
	Failures     int
	FailingSince time.Time
	LastError    string
}

// Deps are what the service uses.
type Deps struct {
	DB        *db.DB
	Events    *events.Catalog
	Settings  *settings.Store
	Jobs      *jobs.System
	Services  *services.Service
	Lists     *lists.Service
	Fetch     *sources.Fetcher // 30 s per request
	Endpoints sources.Endpoints
	Now       func() time.Time
	Log       *slog.Logger
}

// Service is the catalog.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

// Subject is the catalog's event subject.
var Subject = events.Subject{Type: "routing", ID: "catalog"}

// Status reads every source's catalog.
func (s *Service) Status(ctx context.Context) ([]SourceStatus, error) {
	rows, err := store.CatalogSources(ctx, s.d.DB.R)
	if err != nil {
		return nil, err
	}
	out := make([]SourceStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, SourceStatus{
			Source: r.Source, RefreshedAt: r.RefreshedAt.Time, Entries: r.Entries, Failures: r.Failures,
			FailingSince: r.FailingSince.Time, LastError: r.LastError,
		})
	}
	return out, nil
}

// Holder is a catalog selector holding some of the names looked up.
type Holder struct {
	Selector string
	Names    int // how many of the names it holds
}

// lookupChunk bounds the names of one lookup query.
const lookupChunk = 500

// WhereAre finds the catalog selectors (other than except) holding the names
// directly, the biggest holder first, and the catalog date of the biggest
// one's source.
func (s *Service) WhereAre(ctx context.Context, names []string, except string) ([]Holder, time.Time, error) {
	srcs, err := store.CatalogSources(ctx, s.d.DB.R)
	if err != nil {
		return nil, time.Time{}, err
	}
	refreshed := map[string]time.Time{}
	for _, r := range srcs {
		refreshed[r.Source] = r.RefreshedAt.Time
	}
	held := map[string]map[string]bool{} // selector → names
	source := map[string]string{}
	sp, err := s.speller(ctx, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	var sites []string
	var rows []store.Holding
	for i := 0; i < len(names); i += lookupChunk {
		part, err := store.CatalogHolders(ctx, s.d.DB.R, names[i:min(i+lookupChunk, len(names))])
		if err != nil {
			return nil, time.Time{}, err
		}
		rows = append(rows, part...)
		for _, r := range part {
			if r.Source != "v2fly" {
				sites = append(sites, r.Name)
			}
		}
	}
	if err := sp.load(ctx, sites); err != nil {
		return nil, time.Time{}, err
	}
	for _, r := range rows {
		sel := "v2fly:" + r.Name
		if r.Source != "v2fly" {
			sel = sp.spell(portalOf(r.Source), r.Name)
		}
		if sel == except {
			continue
		}
		if held[sel] == nil {
			held[sel] = map[string]bool{}
		}
		held[sel][r.Domain] = true
		source[sel] = r.Source
	}
	out := make([]Holder, 0, len(held))
	for sel, n := range held {
		out = append(out, Holder{Selector: sel, Names: len(n)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Names != out[j].Names {
			return out[i].Names > out[j].Names
		}
		return out[i].Selector < out[j].Selector
	})
	if len(out) == 0 {
		return nil, time.Time{}, nil
	}
	return out, refreshed[source[out[0].Selector]], nil
}

func portalOf(source string) string {
	if len(source) > len("iplist:") {
		return source[len("iplist:"):]
	}
	return ""
}

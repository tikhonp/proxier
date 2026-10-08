package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/conf"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// JobRefresh is the catalog refresh, and its schedule.
const JobRefresh = "routing.catalog_refresh"

// FailingAfter is how many failed refreshes of a source in a row notify:
// three days with the daily schedule.
const FailingAfter = 3

// catalogPayload carries each source's result to the finish step.
type catalogPayload struct {
	Wrote  map[string]int  `json:"wrote,omitempty"`  // source → entries of its new generation
	Failed map[string]bool `json:"failed,omitempty"` // sources that failed
}

func (s *Service) request(actor string) jobs.Request {
	return jobs.Request{Type: JobRefresh, CoalescingKey: "catalog", Subject: Subject, CreatedBy: actor}
}

// RefreshNow queues a catalog refresh now (one already queued takes it).
func (s *Service) RefreshNow(ctx context.Context, actor string) (int64, error) {
	e, err := s.d.Jobs.EnqueueNow(ctx, s.request(actor))
	return e.ID, err
}

// ActiveJob is the catalog refresh that is queued or running (0: none).
func (s *Service) ActiveJob(ctx context.Context) (int64, error) {
	list, err := s.d.Jobs.List(ctx, jobs.Filter{Type: JobRefresh, States: []jobs.State{jobs.Queued, jobs.Running, jobs.Interrupted}, Limit: 1})
	if err != nil || len(list) == 0 {
		return 0, err
	}
	return list[0].ID, nil
}

func (s *Service) refreshing(ctx context.Context) (bool, error) {
	id, err := s.ActiveJob(ctx)
	return id != 0, err
}

// JobTypes is routing.catalog_refresh.
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{{Name: JobRefresh, Queue: jobs.Refresh, MaxAttempts: 1, Steps: []jobs.Step{
		{Name: "v2fly", Run: s.runV2fly},
		{Name: "iplist", Run: s.runIplist},
		{Name: "finish", Run: s.runFinish},
	}}}
}

// Schedules: daily at routing.catalog_at.
func (s *Service) Schedules() []jobs.Schedule {
	return []jobs.Schedule{{
		Name: JobRefresh, At: "04:30", Setting: conf.CatalogAt, Jitter: 5 * time.Minute,
		Request: func(context.Context) (jobs.Request, error) { return s.request(""), nil },
	}}
}

func (s *Service) payload(r *jobs.Run) (catalogPayload, error) {
	var p catalogPayload
	if err := r.Payload(&p); err != nil {
		return p, jobs.Permanent(err)
	}
	if p.Wrote == nil {
		p.Wrote = map[string]int{}
	}
	if p.Failed == nil {
		p.Failed = map[string]bool{}
	}
	return p, nil
}

func (s *Service) runV2fly(ctx context.Context, r *jobs.Run) error {
	p, err := s.payload(r)
	if err != nil {
		return err
	}
	cur, err := store.GetCatalogSource(ctx, s.d.DB.R, "v2fly")
	if err != nil {
		return err
	}
	g, ferr := s.fetchV2fly(ctx, cur)
	if err := s.settle(ctx, r, &p, "v2fly", g, ferr); err != nil {
		return err
	}
	return r.SavePayload(ctx, p)
}

func (s *Service) runIplist(ctx context.Context, r *jobs.Run) error {
	p, err := s.payload(r)
	if err != nil {
		return err
	}
	for _, portal := range s.d.Endpoints.Portals {
		source := "iplist:" + portal.Name
		cur, err := store.GetCatalogSource(ctx, s.d.DB.R, source)
		if errors.Is(err, store.ErrNotFound) {
			continue // a portal the catalog doesn't know
		}
		if err != nil {
			return err
		}
		g, ferr := s.fetchIplist(ctx, portal, cur)
		if err := s.settle(ctx, r, &p, source, g, ferr); err != nil {
			return err
		}
	}
	return r.SavePayload(ctx, p)
}

// settle writes a fetched generation, or records the source unchanged or
// failed. A source that fails keeps its generation and the step goes on:
// only a database error (or a shutdown) ends it.
func (s *Service) settle(ctx context.Context, r *jobs.Run, p *catalogPayload, source string, g generation, ferr error) error {
	now := db.At(s.d.Now())
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(ferr, errUnchanged):
		r.Log().Info("%s: unchanged", source)
		return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.CatalogUnchanged(ctx, tx, source, now) })
	case ferr != nil:
		text := sources.ErrorText(ferr)
		r.Log().Warn("%s: failed: %s", source, text)
		p.Failed[source] = true
		return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.CatalogFailed(ctx, tx, source, text, now) })
	}
	if err := s.write(ctx, g); err != nil {
		return err
	}
	r.Log().Info("%s: %d entries, %d domains", source, len(g.entries), len(g.domains))
	p.Wrote[source] = len(g.entries)
	return nil
}

// runFinish records catalog_refreshed when a source changed or failed, then
// catalog_refresh_failed once per run of failures for each source failing
// for FailingAfter refreshes in a row.
func (s *Service) runFinish(ctx context.Context, r *jobs.Run) error {
	p, err := s.payload(r)
	if err != nil {
		return err
	}
	actor := r.Info().Actor()
	at := db.At(s.d.Now())
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		srcs, err := store.CatalogSources(ctx, tx)
		if err != nil {
			return err
		}
		if len(p.Wrote)+len(p.Failed) > 0 {
			payload := map[string]any{}
			for _, src := range srcs {
				n := src.Entries
				if p.Failed[src.Source] {
					n = -1
				}
				payload[payloadKey(src.Source)] = n
			}
			if _, err := s.d.Events.Record(ctx, tx, events.Event{Time: at, Type: "routing.catalog_refreshed", Subject: Subject,
				Actor: actor, Payload: payload}); err != nil {
				return err
			}
		}
		for _, src := range srcs {
			if src.Failures < FailingAfter || src.Notified {
				continue
			}
			if err := store.SetCatalogNotified(ctx, tx, src.Source); err != nil {
				return err
			}
			if _, err := s.d.Events.Record(ctx, tx, events.Event{Time: at, Type: "routing.catalog_refresh_failed", Subject: Subject,
				Actor: actor, Payload: map[string]any{"source": src.Source, "error": src.LastError, "since": timeText(src.FailingSince)}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// payloadKey is a source's key in catalog_refreshed: v2fly, iplist_main, …
func payloadKey(source string) string {
	if p := portalOf(source); p != "" {
		return "iplist_" + p
	}
	return source
}

// timeText is a db.Time as payloads carry it, "" for none.
func timeText(t db.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(db.TimeLayout)
}

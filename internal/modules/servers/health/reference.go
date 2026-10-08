package health

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// RefProbe is the answer of one reference address.
type RefProbe struct {
	URL    string `json:"url"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// RefDetail is the stored detail of a reference check.
type RefDetail struct {
	Domestic []RefProbe `json:"domestic"`
	Foreign  []RefProbe `json:"foreign"`
}

// homeSubject is the subject of the reference events.
var homeSubject = events.Subject{Type: "home", ID: "1"}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// probe requests each address in parallel; any 2xx or 3xx answer counts.
func (s *Service) probe(ctx context.Context, urls []string) []RefProbe {
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	out := make([]RefProbe, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = RefProbe{URL: u}
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			resp, err := hc.Do(req)
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			_ = resp.Body.Close()
			out[i].Status = resp.StatusCode
			out[i].OK = resp.StatusCode >= 200 && resp.StatusCode < 400
		}()
	}
	wg.Wait()
	return out
}

func anyOK(ps []RefProbe) bool {
	for _, p := range ps {
		if p.OK {
			return true
		}
	}
	return false
}

// referenceStep checks home's internet. It does nothing when no server is
// being checked. A change of state records the event (offline and foreign
// unreachable are shown on the dashboard; foreign unreachable and the
// recovery notify) and, on the way back, clears every candidate run so
// results from before the outage cannot confirm anything.
func (s *Service) referenceStep(ctx context.Context, r *jobs.Run) error {
	ids, err := store.CheckableServerIDs(ctx, s.DB.R)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		r.Log().Info("No server is being checked: nothing to do")
		return nil
	}
	dom, err := s.Settings.Get(ctx, conf.ReferenceDomestic)
	if err != nil {
		return err
	}
	foreign, err := s.Settings.Get(ctx, conf.ReferenceForeign)
	if err != nil {
		return err
	}
	var d RefDetail
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.Domestic = s.probe(ctx, splitList(dom)) }()
	go func() { defer wg.Done(); d.Foreign = s.probe(ctx, splitList(foreign)) }()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	result := store.HomeOnline
	switch {
	case !anyOK(d.Domestic):
		result = store.HomeOffline
	case !anyOK(d.Foreign):
		result = store.HomeForeignUnreachable
	}
	r.Log().Info("Reference check: %s", result)
	b, _ := json.Marshal(d)
	now := db.At(s.now())
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := store.InsertReference(ctx, tx, now, result, string(b)); err != nil {
			return err
		}
		prev, since, err := store.GetHome(ctx, tx)
		if err != nil {
			return err
		}
		if prev == result {
			return nil
		}
		if err := store.SetHome(ctx, tx, result, now); err != nil {
			return err
		}
		rec := func(typ string, payload map[string]any) error {
			_, err := s.Events.Record(ctx, tx, events.Event{Type: typ, Subject: homeSubject, Actor: r.Info().Actor(), Payload: payload})
			return err
		}
		summary := map[string]any{"domestic": d.Domestic, "foreign": d.Foreign}
		switch result {
		case store.HomeOffline:
			return rec("health.home_offline", summary)
		case store.HomeForeignUnreachable:
			return rec("health.foreign_unreachable", summary)
		}
		// Back online: the verdicts resume with results made from now on.
		if err := store.ResetCandidates(ctx, tx); err != nil {
			return err
		}
		dur := time.Duration(0)
		if !since.IsZero() {
			dur = now.Sub(since.Time).Round(time.Second)
		}
		return rec("health.home_recovered", map[string]any{"duration": dur.String(), "from": prev})
	})
}

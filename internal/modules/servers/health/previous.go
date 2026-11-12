package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Job types of previous IPs (docs/processes/servers/ip-change.md).
const (
	JobPreviousRound = "servers.previous_round"
	JobPreviousTest  = "servers.previous_test"
)

// AfterChangeDelay is how long after an IP change its proxy tests wait: more
// than the records' 60 s TTL, so home's resolver no longer answers with the
// old IP, and the change job that queues them has ended (they skip
// themselves while it runs).
const AfterChangeDelay = 90 * time.Second

// ResetForIPChange makes a server's health unknown in the transaction that
// stores its new IP: results from before the change no longer count, and the
// first verdict after it applies at once. A paused server stays paused.
func (s *Service) ResetForIPChange(ctx context.Context, tx *sqlx.Tx, id int64, actor string) error {
	h, err := store.GetHealth(ctx, tx, id)
	if err != nil {
		return err
	}
	if h.State != "active" || h.Retiring || !h.PausedUntil.IsZero() || h.Health == Unknown {
		return nil
	}
	o := Outcome{Candidate: Unknown, Reason: Reason{Key: "health.reason.ip_changed"}, Summary: map[string]any{"rule": "ip_changed"}}
	return s.recordChange(ctx, tx, id, h.Health, o, db.At(s.now()), actor)
}

// QueueAfterIPChange queues the checks of a server's new IP in tx: the
// self-check behind the change job, which holds the server's key; the proxy
// test, the previous IP test and an on-demand external check (within its
// budget) after AfterChangeDelay. A paused server's resume starts them.
func (s *Service) QueueAfterIPChange(ctx context.Context, tx *sqlx.Tx, id int64, actor string) error {
	h, err := store.GetHealth(ctx, tx, id)
	if err != nil {
		return err
	}
	if h.State != "active" || h.Retiring || !h.PausedUntil.IsZero() {
		return nil
	}
	sid := strconv.FormatInt(id, 10)
	if _, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobSelfcheck, ResourceKey: serverKey(id), CoalescingKey: "selfcheck:" + sid,
		Payload: payload{ServerID: id}, CreatedBy: actor,
	}); err != nil {
		return err
	}
	delay := s.afterChangeDelay()
	if _, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobProxytest, Subject: store.ServerSubject(id), CoalescingKey: "proxytest:" + sid,
		Delay: delay, Payload: payload{ServerID: id}, CreatedBy: actor,
	}); err != nil {
		return err
	}
	if err := s.enqueuePrevious(ctx, tx, id, delay, actor); err != nil {
		return err
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	ok, _, err := s.budget(ctx, tx, id, ExternalOnDemand, cfg, s.now())
	if err != nil || !ok {
		return err
	}
	_, err = s.enqueueExternal(ctx, tx, id, ExternalOnDemand, delay, actor)
	return err
}

func (s *Service) afterChangeDelay() time.Duration {
	if s.ChangeDelay > 0 {
		return s.ChangeDelay
	}
	return AfterChangeDelay
}

func (s *Service) enqueuePrevious(ctx context.Context, tx *sqlx.Tx, id int64, delay time.Duration, actor string) error {
	_, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobPreviousTest, Subject: store.ServerSubject(id), CoalescingKey: "previous:" + strconv.FormatInt(id, 10),
		Delay: delay, Payload: payload{ServerID: id}, CreatedBy: actor,
	})
	return err
}

// previousRoundStep queues a test of every checkable server that keeps a
// previous IP.
func (s *Service) previousRoundStep(ctx context.Context, r *jobs.Run) error {
	ids, err := store.ServersWithPreviousIPs(ctx, s.DB.R)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.DB.Write(ctx, func(tx *sqlx.Tx) error { return s.enqueuePrevious(ctx, tx, id, 0, r.Info().Actor()) }); err != nil {
			return err
		}
	}
	s.Jobs.Kick()
	r.Log().Info("Queued previous IP tests for %d servers", len(ids))
	return nil
}

// previousEndpoint is one endpoint's answer through a previous IP.
type previousEndpoint struct {
	Key         string `json:"key"`
	OK          bool   `json:"ok"`
	Class       string `json:"class,omitempty"`
	Error       string `json:"error,omitempty"`
	FirstByteMS int    `json:"first_byte_ms,omitempty"`
	Kbps        int    `json:"kbps,omitempty"`
}

// PreviousDetail is the detail of a previous IP's result.
type PreviousDetail struct {
	URL       string             `json:"url"`
	Endpoints []previousEndpoint `json:"endpoints"`
}

// FirstError is the first failing endpoint's error, "" when all passed.
func (d PreviousDetail) FirstError() string {
	for _, e := range d.Endpoints {
		if !e.OK {
			return e.Error
		}
	}
	return ""
}

// ParsePreviousDetail reads a stored detail.
func ParsePreviousDetail(raw string) PreviousDetail {
	var d PreviousDetail
	_ = json.Unmarshal([]byte(raw), &d)
	return d
}

// previousTestStep runs the proxy test from home through every previous IP
// of a server, as a client would if the hostname still led there: the
// endpoint's names in TLS and HTTP, the previous IP as the address. Like the
// proxy test it stores nothing while another job changes the server.
func (s *Service) previousTestStep(ctx context.Context, r *jobs.Run) error {
	p, err := s.payloadOf(r)
	if err != nil {
		return err
	}
	busy, err := s.Jobs.BusyExcept(ctx, serverKey(p.ServerID), JobSelfcheck)
	if err != nil {
		return err
	}
	if busy {
		r.Log().Info("Skipped: a job is running on the server")
		return nil
	}
	h, err := store.GetHealth(ctx, s.DB.R, p.ServerID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if h.State != "active" || h.Retiring || !h.PausedUntil.IsZero() {
		r.Log().Info("Skipped: the server is not being checked")
		return nil
	}
	prevs, err := store.PreviousIPs(ctx, s.DB.R, p.ServerID)
	if err != nil || len(prevs) == 0 {
		return err
	}
	setup, err := s.Setup(ctx, p.ServerID)
	if err != nil {
		return err
	}
	rows, err := store.Endpoints(ctx, s.DB.R, p.ServerID)
	if err != nil {
		return err
	}
	eps, err := sealed.OpenEndpoints(s.Vault, rows)
	if err != nil {
		return err
	}
	// A previous IP was the primary address; endpoints on additional
	// addresses never lived there, so they say nothing about it.
	addrs, err := store.ServerAddresses(ctx, s.DB.R, p.ServerID)
	if err != nil {
		return err
	}
	eps = slices.DeleteFunc(eps, func(e endpoint.Endpoint) bool {
		return slices.ContainsFunc(addrs, func(a store.Address) bool { return a.Hostname == e.Host })
	})
	url, timeout, stall, err := conf.ProxyTest(ctx, s.Settings, setup.ProxyTestURL)
	if err != nil {
		return err
	}
	sm := s.seams()

	type outcome struct {
		prev   store.PreviousIP
		ok     bool
		class  string
		detail PreviousDetail
	}
	var outs []outcome
	for _, prev := range prevs {
		o := outcome{prev: prev, ok: len(eps) > 0, detail: PreviousDetail{URL: url}}
		for _, e := range eps {
			opts := proxy.Options{URL: url, Timeout: timeout, Stall: stall, Resolve: map[string]string{
				net.JoinHostPort(e.Host, strconv.Itoa(e.Port)): net.JoinHostPort(prev.IP, strconv.Itoa(e.Port)),
			}}
			if sm.ProxyOptions != nil {
				opts = sm.ProxyOptions(ctx, e, opts)
			}
			res := sm.ProxyTest(ctx, e, opts)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			o.detail.Endpoints = append(o.detail.Endpoints, previousEndpoint{
				Key: e.Key, OK: res.OK, Class: string(res.Class), Error: res.Error, FirstByteMS: res.FirstByteMS, Kbps: res.ThroughputKbps,
			})
			if res.OK {
				r.Log().Info("%s through %s passed: first byte after %d ms, %d kbit/s", prev.IP, e.Key, res.FirstByteMS, res.ThroughputKbps)
				continue
			}
			r.Log().Warn("%s through %s failed (%s): %s", prev.IP, e.Key, res.Class, res.Error)
			if o.ok {
				o.ok, o.class = false, string(res.Class)
			}
		}
		outs = append(outs, o)
	}

	online, err := s.homeOnline(ctx)
	if err != nil {
		return err
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	at := db.At(s.now())
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, o := range outs {
			// Forgotten or switched to while it was being tested.
			cur, err := store.GetPreviousIP(ctx, tx, p.ServerID, o.prev.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			b, _ := json.Marshal(o.detail)
			if err := store.InsertPreviousResult(ctx, tx, store.PreviousResult{
				PreviousID: cur.ID, At: at, OK: o.ok, Class: o.class, Inconclusive: !online, Detail: string(b),
			}); err != nil {
				return err
			}
			if !online {
				continue
			}
			if err := s.applyPrevious(ctx, tx, cur, o.ok, o.detail.FirstError(), cfg.Thresholds.Confirmations, at, r.Info().Actor()); err != nil {
				return err
			}
		}
		return nil
	})
}

// applyPrevious moves a previous IP's state by one conclusive result. The
// first result after unknown applies at once; a change needs as many results
// in a row as a server's health does.
func (s *Service) applyPrevious(ctx context.Context, tx *sqlx.Tx, p store.PreviousIP, ok bool, errText string, confirmations int, at db.Time, actor string) error {
	cand := store.PreviousFails
	if ok {
		cand = store.PreviousWorks
	}
	switch {
	case cand == p.State:
		if p.Candidate.Valid {
			return store.SetPreviousCandidate(ctx, tx, p.ID, "", 0)
		}
		return nil
	case p.State != store.PreviousUnknown:
		count := 1
		if p.Candidate.Valid && p.Candidate.String == cand {
			count = p.CandidateCount + 1
		}
		if count < confirmations {
			return store.SetPreviousCandidate(ctx, tx, p.ID, cand, count)
		}
	}
	if err := store.SetPreviousState(ctx, tx, p.ID, cand, at); err != nil {
		return err
	}
	payload := map[string]any{"ip": p.IP, "from": p.State, "to": cand}
	if !ok {
		payload["error"] = errText
	}
	_, err := s.Events.Record(ctx, tx, events.Event{
		Type: "server.previous_ip_changed", Subject: store.ServerSubject(p.ServerID), Actor: actor, Payload: payload,
	})
	return err
}

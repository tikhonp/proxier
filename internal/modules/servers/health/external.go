package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/checkhost"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// External check kinds.
const (
	ExternalScheduled = "scheduled"
	ExternalOnDemand  = "on_demand"
)

// ErrOverBudget is returned when an on-demand external check would break the
// per-server or hourly limits.
var ErrOverBudget = errors.New("health: the external check is over budget")

// nodeList reads a node setting.
func (s *Service) nodeList(ctx context.Context, key string) ([]string, error) {
	v, err := s.Settings.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return splitList(v), nil
}

// budget says whether an external check of a server may run now: per server
// (a scheduled one is refused within 80 % of the interval of the last real
// one, an on-demand one within on_demand_after) and over all servers per
// rolling hour. Skipped rows do not count.
func (s *Service) budget(ctx context.Context, q sqlx.QueryerContext, id int64, kind string, cfg Config, now time.Time) (bool, string, error) {
	last, ok, err := store.LastExternal(ctx, q, id)
	if err != nil {
		return false, "", err
	}
	if ok {
		window := cfg.OnDemandAfter
		if kind == ExternalScheduled {
			window = cfg.ExternalEvery * 8 / 10
		}
		if now.Sub(last.Time) < window {
			return false, "one was made less than " + window.Round(time.Second).String() + " ago", nil
		}
	}
	n, err := store.ExternalChecksSince(ctx, q, db.At(now.Add(-time.Hour)))
	if err != nil {
		return false, "", err
	}
	if n >= cfg.HourlyCap {
		return false, "the hourly cap of " + strconv.Itoa(cfg.HourlyCap) + " checks is reached", nil
	}
	return true, "", nil
}

// Region of a node in a stored result.
const (
	regionRU     = "ru"
	regionAbroad = "abroad"
)

func errorClass(msg string) string {
	switch strings.ToLower(msg) {
	case "connection timed out":
		return "timeout"
	case "connection refused":
		return "refused"
	case "no answer":
		return "no-answer"
	}
	return "error"
}

// externalStep asks check-host.net whether the server's port answers from the
// configured nodes, within the budget. Over budget, it stores one skipped row;
// when check-host fails, one unavailable row. Either way the evaluation that
// follows knows there is no external data.
func (s *Service) externalStep(ctx context.Context, r *jobs.Run) error {
	p, err := s.payloadOf(r)
	if err != nil {
		return err
	}
	srv, err := store.GetServer(ctx, s.DB.R, p.ServerID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	h, err := store.GetHealth(ctx, s.DB.R, p.ServerID)
	if err != nil {
		return err
	}
	if srv.State != "active" || h.Retiring || !h.PausedUntil.IsZero() {
		p.Skipped = "the server is not being checked"
		return r.SavePayload(ctx, p)
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	actor := r.Info().Actor()

	storeOne := func(class, reason string) error {
		b, _ := json.Marshal(map[string]any{"reason": reason, "kind": p.Kind})
		return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			if err := store.InsertCheckResult(ctx, tx, store.CheckResult{
				ServerID: srv.ID, Kind: "external", Vantage: "check-host", At: db.At(now), Class: class, Detail: string(b),
			}); err != nil {
				return err
			}
			return s.answered(ctx, tx, srv.ID, actor)
		})
	}

	ok, why, err := s.budget(ctx, s.DB.R, srv.ID, p.Kind, cfg, now)
	if err != nil {
		return err
	}
	if !ok {
		r.Log().Info("Skipped: %s", why)
		return storeOne("skipped", why)
	}
	if len(cfg.NodesRU)+len(cfg.Abroad) == 0 {
		if err := s.RefreshNodes(ctx, actor); err != nil {
			r.Log().Warn("Could not list the nodes: %v", err)
			return storeOne("unavailable", err.Error())
		}
		if cfg, err = s.Config(ctx); err != nil {
			return err
		}
	}
	nodes := append(append([]string(nil), cfg.NodesRU...), cfg.Abroad...)
	if len(nodes) == 0 {
		r.Log().Warn("No external nodes are configured")
		return storeOne("unavailable", "no nodes are configured")
	}
	port := 443
	if eps, err := store.Endpoints(ctx, s.DB.R, srv.ID); err == nil && len(eps) > 0 {
		port = eps[0].Port
	}
	target := net.JoinHostPort(srv.IP, strconv.Itoa(port))
	r.Log().Info("Asking %d nodes to connect to %s", len(nodes), target)
	results, err := s.checkhostClient().CheckTCP(ctx, target, nodes)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		r.Log().Warn("check-host: %v", err)
		return storeOne("unavailable", err.Error())
	}
	known, _ := store.CheckhostNodes(ctx, s.DB.R)
	countries := map[string]string{}
	for _, n := range known {
		countries[n.Name] = n.Country
	}
	ru := map[string]bool{}
	for _, n := range cfg.NodesRU {
		ru[n] = true
	}
	at := db.At(s.now()) // after the polling, so a slow check does not skip the next round
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, n := range results {
			region := regionAbroad
			if ru[n.Node] {
				region = regionRU
			}
			class := ""
			if !n.OK {
				class = errorClass(n.Error)
			}
			if n.OK {
				r.Log().Info("%s connected in %d ms", n.Node, n.ConnectMS)
			} else {
				r.Log().Info("%s: %s", n.Node, n.Error)
			}
			b, _ := json.Marshal(nodeDetail{Region: region, Country: countries[n.Node], MS: n.ConnectMS, Error: n.Error})
			if err := store.InsertCheckResult(ctx, tx, store.CheckResult{
				ServerID: srv.ID, Kind: "external", Vantage: n.Node, At: at, OK: n.OK, Class: class, Detail: string(b),
			}); err != nil {
				return err
			}
		}
		return s.answered(ctx, tx, srv.ID, actor)
	})
}

// answered ends the wait for an external check: the awaiting mark goes, and so
// does the delayed evaluation that would have decided without it.
func (s *Service) answered(ctx context.Context, tx *sqlx.Tx, id int64, actor string) error {
	if err := s.setAwaiting(ctx, tx, id, time.Time{}); err != nil {
		return err
	}
	_, err := s.Jobs.CancelQueued(ctx, tx, JobEvaluate, store.ServerSubject(id), actor)
	return err
}

// externalRoundStep queues the scheduled external check of every checked
// server, spread a few seconds apart so check-host is not hit all at once.
func (s *Service) externalRoundStep(ctx context.Context, r *jobs.Run) error {
	ids, err := store.CheckableServerIDs(ctx, s.DB.R)
	if err != nil {
		return err
	}
	for i, id := range ids {
		err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			_, err := s.enqueueExternal(ctx, tx, id, ExternalScheduled, time.Duration(i)*10*time.Second, r.Info().Actor())
			return err
		})
		if err != nil {
			return err
		}
	}
	s.Jobs.Kick()
	r.Log().Info("Queued external checks for %d servers", len(ids))
	return nil
}

// RefreshNodes reads check-host's node list, stores it and, when the node
// settings are empty, fills them with the defaults.
func (s *Service) RefreshNodes(ctx context.Context, actor string) error {
	nodes, err := s.checkhostClient().Nodes(ctx)
	if err != nil {
		return err
	}
	rows := make([]store.CheckhostNode, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, store.CheckhostNode{Name: n.Name, Country: n.Country, City: n.City})
	}
	if err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return store.ReplaceCheckhostNodes(ctx, tx, rows, db.At(s.now()))
	}); err != nil {
		return err
	}
	ru, abroad := DefaultNodes(nodes)
	vals := map[string]string{}
	if cur, err := s.Settings.Get(ctx, conf.ExternalNodesRU); err == nil && cur == "" && len(ru) > 0 {
		vals[conf.ExternalNodesRU] = strings.Join(ru, ",")
	}
	if cur, err := s.Settings.Get(ctx, conf.ExternalNodesAbroad); err == nil && cur == "" && len(abroad) > 0 {
		vals[conf.ExternalNodesAbroad] = strings.Join(abroad, ",")
	}
	if len(vals) == 0 {
		return nil
	}
	return s.Settings.Set(ctx, actor, "servers", vals)
}

// nodesStep is the daily refresh.
func (s *Service) nodesStep(ctx context.Context, r *jobs.Run) error {
	if err := s.RefreshNodes(ctx, r.Info().Actor()); err != nil {
		return err
	}
	r.Log().Info("The node list is refreshed")
	return nil
}

// preferredAbroad is where default nodes abroad come from, in order: one node
// per country, the first by name, until there are three.
var preferredAbroad = []string{"de", "nl", "fi", "se", "pl", "fr", "ch", "at", "gb", "it", "es"}

// DefaultNodes picks the nodes used when none are configured: three in Russia
// (the first by name) and three abroad, one each in Germany, the Netherlands
// and Finland, then in other nearby countries when those have no node.
func DefaultNodes(nodes []checkhost.Node) (ru, abroad []string) {
	sorted := append([]checkhost.Node(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	byCountry := map[string]string{}
	for _, n := range sorted {
		if n.Country == "ru" {
			if len(ru) < 3 {
				ru = append(ru, n.Name)
			}
			continue
		}
		if _, ok := byCountry[n.Country]; !ok {
			byCountry[n.Country] = n.Name
		}
	}
	for _, c := range preferredAbroad {
		if name, ok := byCountry[c]; ok && len(abroad) < 3 {
			abroad = append(abroad, name)
		}
	}
	return ru, abroad
}

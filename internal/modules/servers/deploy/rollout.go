package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// SubscriberName is the event subscriber that advances rollouts.
const SubscriberName = "servers.rollout"

// Rollout errors.
var (
	ErrMixedTemplates = errors.New("deploy: a rollout upgrades the servers of one template")
	ErrNothingToRoll  = errors.New("deploy: no server was chosen")
	ErrRolloutProblem = errors.New("deploy: a server cannot be upgraded; the plan shows why")
)

// RolloutItemPlan is one server of a rollout screen.
type RolloutItemPlan struct {
	ServerID      int64
	Name          string
	From, To      int
	FilesChanged  int
	MissingParams []string
	// Skip is the i18n key of why the server is left out; "" means it will run.
	Skip string
	// Problem is why the target does not render for the server; the rollout
	// cannot start while one server has it.
	Problem string
}

// PlanRollout shows, per server and in name order, what an upgrade to the
// default version of their template would do. The servers must share a template.
func (s *Service) PlanRollout(ctx context.Context, serverIDs []int64) ([]RolloutItemPlan, error) {
	return s.PlanRolloutWith(ctx, serverIDs, nil)
}

// PlanRolloutWith is PlanRollout with values for the parameters servers need,
// by server id: the screen asks again after the admin typed them.
func (s *Service) PlanRolloutWith(ctx context.Context, serverIDs []int64, params map[int64]map[string]string) ([]RolloutItemPlan, error) {
	items, _, err := s.planRollout(ctx, serverIDs, params)
	return items, err
}

// planRollout also returns the template and the version the rollout goes to.
func (s *Service) planRollout(ctx context.Context, serverIDs []int64, params map[int64]map[string]string) ([]RolloutItemPlan, rolloutTarget, error) {
	if len(serverIDs) == 0 {
		return nil, rolloutTarget{}, ErrNothingToRoll
	}
	var servers []store.Server
	seen := map[int64]bool{}
	for _, id := range serverIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		srv, err := store.GetServer(ctx, s.DB.R, id)
		if err != nil {
			return nil, rolloutTarget{}, err
		}
		servers = append(servers, srv)
	}
	tpl := servers[0].TemplateID
	for _, srv := range servers {
		if srv.TemplateID != tpl {
			return nil, rolloutTarget{}, ErrMixedTemplates
		}
	}
	info, err := s.Templates.Get(ctx, tpl)
	if err != nil {
		return nil, rolloutTarget{}, err
	}
	if info.DefaultVersion == 0 {
		return nil, rolloutTarget{}, errors.New("the template has no default version")
	}
	to := rolloutTarget{TemplateID: tpl, Version: info.DefaultVersion}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	var out []RolloutItemPlan
	for _, srv := range servers {
		it := RolloutItemPlan{ServerID: srv.ID, Name: srv.Name, From: srv.TemplateVersion, To: to.Version}
		switch {
		case srv.State != "active" || srv.Retiring():
			it.Skip = "servers.rollout.skip.not_active"
		case srv.TemplateVersion == to.Version:
			it.Skip = "servers.rollout.skip.current"
		default:
			c, err := s.compute(ctx, srv, Target{Kind: KindUpgrade, Version: to.Version, Params: params[srv.ID]}, nil)
			var re *RenderError
			switch {
			case errors.As(err, &re):
				it.Problem = re.Message
			case err != nil:
				return nil, to, err
			default:
				it.FilesChanged = len(c.Plan.Files)
				it.MissingParams = c.Plan.MissingParams
			}
		}
		out = append(out, it)
	}
	return out, to, nil
}

type rolloutTarget struct {
	TemplateID int64
	Version    int
}

// StartRollout starts the upgrade of the chosen servers to the default version
// of their template, one at a time in name order. params are values for the
// parameters that servers need and have no default for, by server id. Servers
// that are not active or already at the version are skipped.
func (s *Service) StartRollout(ctx context.Context, serverIDs []int64, params map[int64]map[string]string, actor string) (int64, error) {
	items, to, err := s.planRollout(ctx, serverIDs, params)
	if err != nil {
		return 0, err
	}
	errs := FieldErrors{}
	type prepared struct {
		plan   RolloutItemPlan
		public map[string]string
		secret map[string]string
	}
	var rows []prepared
	for _, it := range items {
		p := prepared{plan: it, public: map[string]string{}, secret: map[string]string{}}
		if it.Skip == "" {
			if it.Problem != "" {
				return 0, ErrRolloutProblem
			}
			srv, err := store.GetServer(ctx, s.DB.R, it.ServerID)
			if err != nil {
				return 0, err
			}
			c, err := s.compute(ctx, srv, Target{Kind: KindUpgrade, Version: to.Version, Params: params[it.ServerID]}, nil)
			if err != nil {
				return 0, err
			}
			for k, m := range c.Errs {
				errs["server."+strconv.FormatInt(it.ServerID, 10)+"."+k] = m
			}
			for _, par := range c.Man.Parameters {
				v, ok := params[it.ServerID][par.Key]
				if !ok {
					continue
				}
				if par.Secret {
					p.secret[par.Key] = v
				} else {
					p.public[par.Key] = v
				}
			}
		}
		rows = append(rows, p)
	}
	if len(errs) > 0 {
		return 0, errs
	}

	var id int64
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := db.At(s.now())
		var err error
		if id, err = store.InsertRollout(ctx, tx, to.TemplateID, to.Version, actor, now); err != nil {
			return err
		}
		var names []string
		for i, p := range rows {
			it := store.RolloutItem{RolloutID: id, Position: i, ServerID: p.plan.ServerID, FromVersion: p.plan.From, Params: marshalParams(p.public),
				ParamsSecret: sealed.SealRolloutParams(s.Vault, id, i, p.secret), State: "waiting"}
			if p.plan.Skip != "" {
				it.State, it.Error = "skipped", p.plan.Skip
			} else {
				names = append(names, p.plan.Name)
			}
			if err := store.InsertRolloutItem(ctx, tx, it); err != nil {
				return err
			}
		}
		if _, err := s.Events.Record(ctx, tx, events.Event{Type: "server.rollout_started", Subject: rolloutSubject(id), Actor: actor,
			Payload: map[string]any{"servers": names, "to_version": to.Version}}); err != nil {
			return err
		}
		ro, err := store.GetRollout(ctx, tx, id)
		if err != nil {
			return err
		}
		return s.advance(ctx, tx, ro, actor)
	})
	if err != nil {
		return 0, err
	}
	s.Jobs.Kick()
	return id, nil
}

func rolloutSubject(id int64) events.Subject {
	return events.Subject{Type: "rollout", ID: strconv.FormatInt(id, 10)}
}

// advance starts the next waiting item of a running rollout, skipping the
// servers that changed meanwhile; with none left (and none running) the
// rollout is done.
func (s *Service) advance(ctx context.Context, tx *sqlx.Tx, ro store.Rollout, actor string) error {
	if ro.State != "running" {
		return nil
	}
	items, err := store.RolloutItems(ctx, tx, ro.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.State == "running" {
			return nil
		}
	}
	for _, it := range items {
		if it.State != "waiting" {
			continue
		}
		started, err := s.startItem(ctx, tx, ro, it, actor)
		if err != nil {
			return err
		}
		if started {
			return nil
		}
	}
	if err := store.SetRolloutState(ctx, tx, ro.ID, "done"); err != nil {
		return err
	}
	return s.finishRollout(ctx, tx, ro.ID, actor)
}

// startItem enqueues the upgrade of one item's server. A server that is no
// longer active, or is at the version already, is skipped instead.
func (s *Service) startItem(ctx context.Context, tx *sqlx.Tx, ro store.Rollout, it store.RolloutItem, actor string) (bool, error) {
	srv, err := store.GetServer(ctx, tx, it.ServerID)
	if err != nil {
		return false, err
	}
	skip := ""
	switch {
	case srv.State != "active" || srv.Retiring():
		skip = "servers.rollout.skip.not_active"
	case srv.TemplateVersion == ro.ToVersion:
		skip = "servers.rollout.skip.current"
	}
	if skip != "" {
		return false, store.SetRolloutItem(ctx, tx, ro.ID, it.Position, "skipped", 0, skip)
	}
	public := store.ParseParams(it.Params)
	secret, err := sealed.OpenRolloutParams(s.Vault, ro.ID, it.Position, it.ParamsSecret)
	if err != nil {
		return false, err
	}
	p := deployPayload{
		base:      base{ServerID: srv.ID, Kind: KindUpgrade, Rollout: ro.ID},
		ToVersion: ro.ToVersion, Params: public,
	}
	secrets := map[string]string{}
	for k, v := range secret {
		secrets[paramSecretPrefix+k] = v
		p.SecretParams = append(p.SecretParams, k)
	}
	sort.Strings(p.SecretParams)
	e, err := s.Jobs.Enqueue(ctx, tx, jobs.Request{Type: JobDeploy, Payload: p, Secrets: secrets, ResourceKey: serverKey(srv.ID), CreatedBy: actor})
	if err != nil {
		return false, err
	}
	return true, store.SetRolloutItem(ctx, tx, ro.ID, it.Position, "running", e.ID, "")
}

// finishRollout records server.rollout_finished once, when the rollout is no
// longer running and no item is.
func (s *Service) finishRollout(ctx context.Context, tx *sqlx.Tx, id int64, actor string) error {
	ro, err := store.GetRollout(ctx, tx, id)
	if err != nil {
		return err
	}
	if ro.State == "running" {
		return nil
	}
	items, err := store.RolloutItems(ctx, tx, id)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, it := range items {
		if it.State == "running" {
			return nil
		}
		counts[it.State]++
	}
	first, err := store.FinishRollout(ctx, tx, id, db.At(s.now()))
	if err != nil || !first {
		return err
	}
	_, err = s.Events.Record(ctx, tx, events.Event{Type: "server.rollout_finished", Subject: rolloutSubject(id), Actor: actor,
		Payload: map[string]any{"state": ro.State, "done": counts["done"], "failed": counts["failed"], "not_started": counts["waiting"], "skipped": counts["skipped"]}})
	return err
}

// StopRollout stops a rollout: nothing new starts. The running item finishes
// on its own and the subscriber records its result and the end; with no item
// running the rollout ends at once.
func (s *Service) StopRollout(ctx context.Context, id int64, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		ro, err := store.GetRollout(ctx, tx, id)
		if err != nil {
			return err
		}
		if ro.State != "running" {
			return ErrNotAllowed
		}
		if err := store.SetRolloutState(ctx, tx, id, "cancelled"); err != nil {
			return err
		}
		return s.finishRollout(ctx, tx, id, actor)
	})
}

// Subscriber is servers.rollout: it moves a rollout on when the deploy job of
// its running item succeeded or failed. Delivery is at least once; an item
// that is no longer running with that job is left alone.
func (s *Service) Subscriber() events.Subscriber {
	return events.Subscriber{
		Name: SubscriberName, Types: []string{"server.redeployed", "server.redeploy_failed"},
		Handle: s.onRolloutEvent,
	}
}

func (s *Service) onRolloutEvent(ctx context.Context, tx *sqlx.Tx, e events.Event) error {
	rid := payloadInt(e.Payload, "rollout")
	jobID, ok := jobOf(e.Actor)
	if rid == 0 || !ok {
		return nil
	}
	ro, err := store.GetRollout(ctx, tx, rid)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	it, found, err := store.RolloutItemForJob(ctx, tx, rid, jobID)
	if err != nil {
		return err
	}
	if !found || it.State != "running" {
		return nil
	}
	if e.Type == "server.redeployed" {
		if err := store.SetRolloutItem(ctx, tx, rid, it.Position, "done", 0, ""); err != nil {
			return err
		}
		if err := s.advance(ctx, tx, ro, events.ActorSystem); err != nil {
			return err
		}
	} else {
		msg, _ := e.Payload["error"].(string)
		if cancelled, _ := e.Payload["cancelled"].(bool); cancelled {
			msg = "cancelled"
		}
		if err := store.SetRolloutItem(ctx, tx, rid, it.Position, "failed", 0, msg); err != nil {
			return err
		}
		if ro.State == "running" {
			if err := store.SetRolloutState(ctx, tx, rid, "stopped"); err != nil {
				return err
			}
		}
	}
	return s.finishRollout(ctx, tx, rid, events.ActorSystem)
}

func payloadInt(p map[string]any, key string) int64 {
	switch v := p[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

// jobOf reads the id of the job from an actor like "job:42".
func jobOf(actor string) (int64, bool) {
	rest, ok := strings.CutPrefix(actor, "job:")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil
}

package pages

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) registerRollout(r web.Routes) {
	r.Admin.POST("/servers/rollout/plan", h.rolloutPlan)
	r.Admin.POST("/servers/rollout", h.rolloutStart)
	r.Admin.GET("/servers/rollouts/:id", h.rolloutPage)
	r.Admin.POST("/servers/rollouts/:id/stop", h.rolloutStop)
}

// ------------------------------------------------------------ the plan screen

type rolloutPlanItem struct {
	ID         int64
	Name, Flag string
	From, To   string
	Files      string
	Skip       string // translated; "" when it will run
	Problem    string
	Fields     []paramField
}

type rolloutPlanView struct {
	Items     []rolloutPlanItem
	ServerIDs []int64
	To        int
	TemplateN string
	CanStart  bool
	Err       string
}

// serverIDsOf reads the servers a rollout form lists.
func serverIDsOf(c *echo.Context) []int64 {
	var ids []int64
	if form, err := c.FormValues(); err == nil {
		for _, raw := range form["server"] {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// rolloutParams reads param.<server id>.<key> values.
func rolloutParams(c *echo.Context) map[int64]map[string]string {
	out := map[int64]map[string]string{}
	form, err := c.FormValues()
	if err != nil {
		return out
	}
	for k, vs := range form {
		rest, ok := strings.CutPrefix(k, "param.")
		if !ok || len(vs) == 0 {
			continue
		}
		idStr, key, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			continue
		}
		if out[id] == nil {
			out[id] = map[string]string{}
		}
		out[id][key] = vs[0]
	}
	return out
}

func (h *handler) rolloutPlan(c *echo.Context) error {
	return h.renderRolloutPlan(c, http.StatusOK, nil)
}

func (h *handler) renderRolloutPlan(c *echo.Context, status int, refused deploy.FieldErrors) error {
	ctx := c.Request().Context()
	h.noStore(c)
	ids := serverIDsOf(c)
	params := rolloutParams(c)
	items, err := h.Deploy.PlanRolloutWith(ctx, ids, params)
	v := rolloutPlanView{ServerIDs: ids}
	switch {
	case errors.Is(err, deploy.ErrMixedTemplates):
		v.Err = i18n.T(ctx, "servers.rollout.err.mixed")
	case errors.Is(err, deploy.ErrNothingToRoll):
		v.Err = i18n.T(ctx, "servers.rollout.err.none")
	case err != nil:
		return notFound(err)
	}
	// The parameter fields come from the version they are for.
	var man *manifest.Manifest
	if len(items) > 0 {
		first, err := store.GetServer(ctx, h.Store.DB.R, items[0].ServerID)
		if err != nil {
			return err
		}
		v.To = items[0].To
		if ver, err := h.Templates.Version(ctx, first.TemplateID, items[0].To); err == nil {
			man, _ = manifest.Parse(ver.Files[manifest.Name])
		}
		v.TemplateN = first.TemplateName
	}
	v.CanStart = len(items) > 0
	runnable := 0
	for _, it := range items {
		row := rolloutPlanItem{ID: it.ServerID, Name: it.Name, From: vlabel(it.From), To: vlabel(it.To), Problem: it.Problem}
		if srv, err := store.GetServer(ctx, h.Store.DB.R, it.ServerID); err == nil {
			row.Flag = countryFlag(srv.Country)
		}
		switch {
		case it.Skip != "":
			row.Skip = i18n.T(ctx, it.Skip)
		case it.Problem != "":
			v.CanStart = false
		default:
			runnable++
			row.Files = i18n.From(ctx).N("servers.plan.sum.files", int64(it.FilesChanged))
			for _, key := range it.MissingParams {
				pf := paramField{Name: "param." + i64(it.ServerID) + "." + key, Label: key}
				if man != nil {
					for _, p := range man.Parameters {
						if p.Key == key {
							pf = paramField{Name: "param." + i64(it.ServerID) + "." + key, Label: p.Label, Type: p.Type, Help: p.Help, Required: p.Required, Secret: p.Secret, Options: p.Options}
						}
					}
				}
				if !pf.Secret {
					pf.Value = params[it.ServerID][key]
				}
				if msg, ok := refused["server."+i64(it.ServerID)+".param."+key]; ok {
					pf.Err = i18n.T(ctx, msg.Key, msg.Args)
				}
				row.Fields = append(row.Fields, pf)
			}
			if len(it.MissingParams) > 0 {
				v.CanStart = false
			}
		}
		v.Items = append(v.Items, row)
	}
	if runnable == 0 {
		v.CanStart = false
	}
	title := i18n.T(ctx, "servers.rollout.plan.title", i18n.Args{"n": len(items), "to": vlabel(v.To)})
	return web.Render(c, status, rolloutPlanPage(h.shell(c, title, "/servers"), v))
}

func (h *handler) rolloutStart(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := h.Deploy.StartRollout(ctx, serverIDsOf(c), rolloutParams(c), "admin")
	var fe deploy.FieldErrors
	switch {
	case errors.As(err, &fe):
		return h.renderRolloutPlan(c, http.StatusUnprocessableEntity, fe)
	case errors.Is(err, deploy.ErrMixedTemplates), errors.Is(err, deploy.ErrNothingToRoll), errors.Is(err, deploy.ErrRolloutProblem):
		return h.renderRolloutPlan(c, http.StatusUnprocessableEntity, nil)
	case err != nil:
		return notFound(err)
	}
	return web.Redirect(c, "/servers/rollouts/"+i64(id))
}

// ----------------------------------------------------------------- progress

type rolloutRow struct {
	Position       int
	Name, Flag     string
	Href, JobHref  string
	Kind, Word     string
	Version, Step  string
	Current, Error bool
}

type rolloutView struct {
	ID        int64
	Title     string
	Note      string
	Kind      string // marker kind of the rollout
	Word      string
	Running   bool
	Rows      []rolloutRow
	Stop      *ui.Action
	Summary   string
	FinishedN string
}

func (h *handler) rolloutPage(c *echo.Context) error {
	id, err := h.id(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v, err := h.buildRollout(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, rolloutProgressPage(h.shell(c, v.Title, "/servers"), v))
}

func (h *handler) buildRollout(ctx context.Context, id int64) (rolloutView, error) {
	ro, err := store.GetRollout(ctx, h.Store.DB.R, id)
	if err != nil {
		return rolloutView{}, err
	}
	items, err := store.RolloutItems(ctx, h.Store.DB.R, id)
	if err != nil {
		return rolloutView{}, err
	}
	loc := i18n.From(ctx)
	v := rolloutView{ID: id, Running: ro.State == "running" || ro.FinishedAt.IsZero()}
	v.Title = i18n.T(ctx, "servers.rollout.title", i18n.Args{"to": vlabel(ro.ToVersion)})
	v.Word = i18n.T(ctx, "servers.rollout.state."+ro.State)
	v.Kind = map[string]string{"running": "running", "done": "ok", "stopped": "broken", "cancelled": "off"}[ro.State]
	counts := map[string]int{}
	var prevName string
	for _, it := range items {
		counts[it.State]++
		srv, err := store.GetServer(ctx, h.Store.DB.R, it.ServerID)
		if err != nil {
			return v, err
		}
		row := rolloutRow{
			Position: it.Position, Name: srv.Name, Flag: countryFlag(srv.Country), Href: serverHref(srv.ID),
			Word: i18n.T(ctx, "servers.rollout.item."+it.State), Version: vlabel(it.FromVersion) + " → " + vlabel(ro.ToVersion),
			Current: it.State == "running", Error: it.State == "failed",
		}
		row.Kind = map[string]string{"waiting": "unknown", "running": "running", "done": "ok", "failed": "broken", "skipped": "off"}[it.State]
		if it.JobID.Valid {
			row.JobHref = "/jobs/" + i64(it.JobID.Int64)
		}
		switch it.State {
		case "waiting":
			if ro.State == "running" && prevName != "" {
				row.Step = i18n.T(ctx, "servers.rollout.waits", i18n.Args{"name": prevName})
			} else {
				row.Step = i18n.T(ctx, "servers.rollout.not_started")
			}
		case "skipped":
			row.Step = i18n.T(ctx, it.Error)
		case "failed":
			row.Step = it.Error
		case "done":
			row.Step = i18n.T(ctx, "servers.rollout.done_step")
		case "running":
			if it.JobID.Valid {
				if steps, err := h.Jobs.Steps(ctx, it.JobID.Int64); err == nil {
					for _, st := range steps {
						if st.State == string(jobs.Running) || st.State == "running" {
							row.Step = i18n.T(ctx, "job.servers.deploy.step."+st.Name)
						}
					}
				}
			}
		}
		prevName = srv.Name
		v.Rows = append(v.Rows, row)
	}
	sort.SliceStable(v.Rows, func(i, j int) bool { return v.Rows[i].Position < v.Rows[j].Position })
	v.Summary = i18n.T(ctx, "servers.rollout.summary", i18n.Args{
		"done": counts["done"], "failed": counts["failed"], "waiting": counts["waiting"], "skipped": counts["skipped"],
	})
	v.Note = i18n.T(ctx, "servers.rollout.note")
	if !ro.FinishedAt.IsZero() {
		v.FinishedN = i18n.T(ctx, "servers.rollout.finished_at", i18n.Args{"time": loc.Time(ro.FinishedAt.Time)})
	}
	if ro.State == "running" {
		v.Stop = &ui.Action{
			ID: "rollout.stop", Label: "servers.rollout.stop", Method: "POST", Href: "/servers/rollouts/" + i64(id) + "/stop",
			Confirm: &ui.Confirm{Strength: 1, Title: i18n.T(ctx, "servers.rollout.stop"), Body: i18n.T(ctx, "servers.rollout.stop.body")},
		}
	}
	return v, nil
}

func (h *handler) rolloutStop(c *echo.Context) error {
	id, err := h.id(c)
	if err != nil {
		return err
	}
	switch err := h.Deploy.StopRollout(c.Request().Context(), id, "admin"); {
	case errors.Is(err, store.ErrNotFound):
		return echo.ErrNotFound
	case errors.Is(err, deploy.ErrNotAllowed):
		// already over: the page says so
	case err != nil:
		return err
	}
	return web.Redirect(c, "/servers/rollouts/"+i64(id))
}

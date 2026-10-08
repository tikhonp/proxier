package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// cutoffReasons are the texts of links' skip reasons.
var cutoffReasons = map[string]string{
	links.ReasonNotInService: "cutoff.reason.not_in_service",
	links.ReasonNothing:      "cutoff.reason.nothing",
}

type cutoffPlanServer struct {
	Name, Kind, Word string
}

type cutoffPlanView struct {
	L      links.Link
	Rotate []cutoffPlanServer
	Skip   []cutoffPlanServer
	Others string
}

type cutoffItemView struct {
	Position         int
	Name, Kind, Word string
	Detail           string
	JobHref          string
	RetryHref        string
}

// cutoffView is the Cut-off area of the link page.
type cutoffView struct {
	LinkID  int64
	Started string
	Items   []cutoffItemView
	Poll    bool // a rotation waits or runs
}

// cutoffPlan is the page styled as a dialog that Cut off opens.
func (h *handler) cutoffPlan(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, err := h.Links.PlanCutOff(ctx, l.ID)
	if err != nil {
		return cutoffErr(err)
	}
	loc := h.loc(ctx)
	v := cutoffPlanView{L: l}
	for _, s := range p.Rotate {
		v.Rotate = append(v.Rotate, cutoffPlanServer{Name: s.Name, Kind: healthKind(s.Health), Word: loc.T("subs.health." + s.Health)})
	}
	for _, s := range p.Skip {
		ps := cutoffPlanServer{Name: s.Name, Kind: "off", Word: loc.T(cutoffReasons[s.Reason])}
		if s.Health != "" {
			ps.Kind = healthKind(s.Health)
		}
		v.Skip = append(v.Skip, ps)
	}
	switch {
	case len(p.Rotate) == 0:
	case p.OtherLinks == 0:
		v.Others = loc.T("cutoff.others.none")
	default:
		v.Others = loc.T("cutoff.others", i18n.Args{
			"links": loc.N("cutoff.others.links", int64(p.OtherLinks)), "subs": loc.N("cutoff.others.subs", int64(p.OtherSubs)),
		})
	}
	return web.Render(c, http.StatusOK, cutoffPlanPage(h.shell(c, loc.T("cutoff.title", i18n.Args{"name": l.Name}), "/links"), v))
}

func (h *handler) cutoffStart(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	if _, err := h.Links.CutOff(c.Request().Context(), l.ID, events.ActorAdmin); err != nil {
		return cutoffErr(err)
	}
	return web.Redirect(c, linkHref(l.ID))
}

// cutoffStatus is the Cut-off area alone, for its polling.
func (h *handler) cutoffStatus(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	v, ok, err := h.cutoffView(c.Request().Context(), l.ID)
	if err != nil {
		return err
	}
	if !ok {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, cutoffArea(v))
}

func (h *handler) cutoffRetry(c *echo.Context) error {
	l, err := h.loadLink(c)
	if err != nil {
		return err
	}
	pos, err := strconv.Atoi(c.Param("position"))
	if err != nil {
		return echo.ErrNotFound
	}
	if err := h.Links.RetryCutOff(c.Request().Context(), l.ID, pos, events.ActorAdmin); err != nil {
		return cutoffErr(err)
	}
	return web.Redirect(c, linkHref(l.ID))
}

// cutoffView is the latest cut-off of the link; false when it has none.
func (h *handler) cutoffView(ctx context.Context, linkID int64) (cutoffView, bool, error) {
	co, ok, err := h.Links.LatestCutOff(ctx, linkID)
	if err != nil || !ok {
		return cutoffView{}, ok, err
	}
	loc := h.loc(ctx)
	v := cutoffView{LinkID: linkID, Poll: co.Busy(), Started: loc.T("cutoff.area.started", i18n.Args{
		"time": loc.Time(co.CreatedAt), "ago": loc.Ago(co.CreatedAt), "actor": co.CreatedBy,
	})}
	for _, it := range co.Items {
		iv := cutoffItemView{Position: it.Position, Name: it.ServerName, Word: loc.T("cutoff.state." + it.State)}
		if it.JobID != 0 {
			iv.JobHref = "/jobs/" + i64(it.JobID)
		}
		switch it.State {
		case "waiting":
			iv.Kind = "unknown"
		case "running":
			iv.Kind = "running"
		case "done":
			iv.Kind = "ok"
		case "failed":
			iv.Kind = "broken"
			iv.Detail = loc.T("cutoff.failed", i18n.Args{"error": it.Error})
			iv.RetryHref = linkHref(linkID) + "/cutoff/" + strconv.Itoa(it.Position) + "/retry"
		case "skipped":
			iv.Kind = "off"
			iv.Detail = loc.T("cutoff.skipped", i18n.Args{"reason": loc.T(cutoffReasons[it.Error])})
		}
		v.Items = append(v.Items, iv)
	}
	return v, true, nil
}

// cutoffBand is the alert band's Cut off line: which servers it rotates.
func (h *handler) cutoffBand(ctx context.Context, l links.Link) (string, error) {
	p, err := h.Links.PlanCutOff(ctx, l.ID)
	if err != nil {
		return "", err
	}
	loc := h.loc(ctx)
	if len(p.Rotate) == 0 {
		return loc.T("cutoff.band.nothing"), nil
	}
	names := make([]string, len(p.Rotate))
	for i, s := range p.Rotate {
		names[i] = s.Name
	}
	return loc.T("cutoff.band.text", i18n.Args{"servers": strings.Join(names, ", ")}), nil
}

// cutoffErr maps the cut-off's errors: none without a rotator, 409 for a
// deleted link or a server that can't be retried.
func cutoffErr(err error) error {
	switch {
	case errors.Is(err, links.ErrNoRotator):
		return echo.ErrNotFound
	case errors.Is(err, links.ErrNotRetryable):
		return echo.NewHTTPError(http.StatusConflict, "only a failed rotation can be retried")
	}
	return actionErr(err)
}

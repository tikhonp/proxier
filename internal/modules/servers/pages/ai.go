package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/aicheck"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type aiRow struct {
	Server, Endpoint, Service, Href                           string
	IP, Score, Raw, Country, Eligibility, Reachability, State string
	Observed, Lookup, Attempt, Error, Flags                   string
	Rank                                                      int
	Value                                                     float64
}

type aiView struct {
	ServerID int64
	Enabled  bool
	Rows     []aiRow
	PollHref string
	Chips    []ui.Chip
}

func aiPollTrigger(v aiView) string {
	if v.PollHref != "" {
		return "every 2s"
	}
	return "none"
}

func (h *handler) aiView(ctx context.Context, id int64, mode string) (aiView, error) {
	on, err := h.AI.Enabled(ctx)
	v := aiView{ServerID: id, Enabled: on}
	if err != nil || !on {
		return v, err
	}
	rows, err := h.AI.Read(ctx, id)
	if err != nil {
		return v, err
	}
	for _, r := range rows {
		if mode != "" && (mode == "ip" && r.Service != aicheck.Claude || mode != "ip" && r.Service != mode) {
			continue
		}
		rowMode := mode
		if rowMode == "" {
			rowMode = r.Service
		}
		v.Rows = append(v.Rows, aiRowOf(ctx, r, rowMode))
	}
	return v, nil
}

func aiRowOf(ctx context.Context, r aicheck.Snapshot, mode string) aiRow {
	loc := i18n.From(ctx)
	v := aiRow{Endpoint: r.EndpointKey, Service: i18n.T(ctx, "ai.service."+r.Service), IP: r.CurrentIP, Score: i18n.T(ctx, "ai.no_score"), Rank: 2, State: i18n.T(ctx, "ai.state.missing")}
	if r.Attempt.ID > 0 {
		v.State = i18n.T(ctx, "ai.state.failed")
		if r.Service == aicheck.OpenAIAPI {
			v.State = i18n.T(ctx, "ai.state.checked")
		}
		v.Attempt = loc.Time(r.Attempt.AttemptedAt.Time)
		v.Reachability = i18n.T(ctx, "ai.unreachable")
		if r.Attempt.Reachable {
			v.Reachability = i18n.T(ctx, "ai.responded")
		}
		if r.Attempt.HTTPStatus > 0 {
			v.Reachability += " · HTTP " + strconv.Itoa(r.Attempt.HTTPStatus)
		}
		if r.Attempt.Error != "" {
			v.Error = aiError(ctx, r.Attempt.Error)
		}
	}
	if r.Success != nil && !r.Invalidated && r.Success.IP == r.CurrentIP {
		v.Raw = strconv.FormatFloat(*r.Success.RawScore, 'f', -1, 64)
		v.Country = r.Success.Country
		v.Eligibility = i18n.T(ctx, "ai.eligibility."+aicheck.Eligibility(v.Country))
		v.Observed = loc.Time(r.Success.ObservedAt.Time)
		v.Lookup = loc.Time(r.Success.LookupAt.Time)
		v.State = i18n.T(ctx, "ai.state.stale")
		if r.Fresh {
			v.State = i18n.T(ctx, "ai.state.fresh")
			v.Rank = 1
		}
		var rep aicheck.Reputation
		if json.Unmarshal([]byte(r.Success.Flags), &rep) == nil {
			for _, flag := range []struct {
				Key   string
				Value *bool
			}{{"datacenter", rep.Datacenter}, {"residential", rep.Residential}, {"vpn", rep.VPN}, {"proxy", rep.Proxy}, {"tor", rep.Tor}, {"crawler", rep.Crawler}, {"abuser", rep.Abuser}, {"mobile", rep.Mobile}} {
				if flag.Value == nil {
					continue
				}
				word := "ai.no"
				if *flag.Value {
					word = "ai.yes"
				}
				if v.Flags != "" {
					v.Flags += " · "
				}
				v.Flags += i18n.T(ctx, "ai.flag."+flag.Key) + ": " + i18n.T(ctx, word)
			}
		}
	} else if r.Success != nil {
		v.State = i18n.T(ctx, "ai.state.changed")
	}
	if score, ok := r.Score(mode); ok {
		v.Score = strconv.FormatFloat(score, 'f', -1, 64) + " / 100"
		v.Value = score
		if aicheck.Eligibility(v.Country) != "unknown" {
			v.Rank = 0
		}
	}
	return v
}

func aiError(ctx context.Context, err string) string {
	if strings.HasPrefix(err, "trace HTTP ") || strings.HasPrefix(err, "API HTTP ") {
		return i18n.T(ctx, "ai.error.http", i18n.Args{"status": strings.TrimPrefix(strings.TrimPrefix(err, "trace HTTP "), "API HTTP ")})
	}
	if strings.HasPrefix(err, "reputation HTTP ") {
		return i18n.T(ctx, "ai.error.reputation_http", i18n.Args{"status": strings.TrimPrefix(err, "reputation HTTP ")})
	}
	key := map[string]string{"proxy dialer failed": "proxy", "trace request failed": "trace", "API request failed": "api", "reputation request failed": "reputation", "reputation rate limited": "limited"}[err]
	if key == "" {
		key = "invalid"
	}
	return i18n.T(ctx, "ai.error."+key)
}

func (h *handler) runAI(c *echo.Context) error {
	srv, err := h.activeServer(c)
	if err != nil {
		return err
	}
	_, err = h.AI.Queue(c.Request().Context(), srv.ID, web.Actor(c.Request().Context()))
	if errors.Is(err, aicheck.ErrDisabled) {
		return echo.NewHTTPError(http.StatusConflict, i18n.T(c.Request().Context(), "ai.disabled"))
	}
	if errors.Is(err, aicheck.ErrNotActive) {
		return echo.NewHTTPError(http.StatusConflict, "server is not active")
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, serverHref(srv.ID)+"/health?airun="+strconv.FormatInt(h.AI.Now().Unix(), 10)+"#ai-results")
}

func (h *handler) aiRanking(c *echo.Context) error {
	ctx := c.Request().Context()
	on, err := h.AI.Enabled(ctx)
	if err != nil {
		return err
	}
	mode := c.QueryParam("score")
	if mode != "claude" && mode != "chatgpt" {
		mode = "ip"
	}
	v := aiView{Enabled: on}
	for _, m := range []string{"ip", "claude", "chatgpt"} {
		v.Chips = append(v.Chips, ui.Chip{Label: i18n.T(ctx, "ai.mode."+m), Href: "/servers/ai?score=" + m, On: mode == m})
	}
	if on {
		servers, err := store.ListServers(ctx, h.Store.DB.R)
		if err != nil {
			return err
		}
		for _, srv := range servers {
			if srv.State != "active" || srv.Retiring() {
				continue
			}
			view, err := h.aiView(ctx, srv.ID, mode)
			if err != nil {
				return err
			}
			for _, row := range view.Rows {
				row.Server = srv.Name
				row.Href = serverHref(srv.ID) + "/health#ai-results"
				v.Rows = append(v.Rows, row)
			}
		}
		sort.SliceStable(v.Rows, func(i, j int) bool {
			a, b := v.Rows[i], v.Rows[j]
			if a.Rank != b.Rank {
				return a.Rank < b.Rank
			}
			if a.Rank < 2 && a.Value != b.Value {
				return a.Value > b.Value
			}
			if a.Server != b.Server {
				return a.Server < b.Server
			}
			return a.Endpoint < b.Endpoint
		})
	}
	return web.Render(c, http.StatusOK, aiRankingPage(h.shell(c, i18n.T(ctx, "ai.ranking"), "/servers"), v))
}

func (h *handler) registerAI(r web.Routes) {
	srv := r.Area(access.Servers).Manage
	srv.GET("/servers/ai", h.aiRanking)
	srv.POST("/servers/:id/ai/run", h.runAI, h.operable)
}

func (h *handler) healthAI(c *echo.Context, v *healthView) error {
	var err error
	v.AI, err = h.aiView(c.Request().Context(), v.S.ID, "")
	if started, e := strconv.ParseInt(c.QueryParam("airun"), 10, 64); e == nil && started <= h.AI.Now().Unix() && h.AI.Now().Unix()-started < int64(3*time.Minute/time.Second) {
		v.AI.PollHref = serverHref(v.S.ID) + "/health?airun=" + strconv.FormatInt(started, 10)
	}
	return err
}

func aiAreaNote(ctx context.Context, show bool) string {
	if !show {
		return ""
	}
	return i18n.T(ctx, "ai.note")
}

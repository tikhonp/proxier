package pages

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/agent"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/agentapi"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func agentHref(id int64) string { return ScriptHref(id) + "/agent" }

// ---- the band ---------------------------------------------------------------

// agentBandView is the open session as the script page shows it.
type agentBandView struct {
	Script    int64
	ID        int64
	Agent     string
	Problem   string // its first line
	Saves     int
	ExpiresAt time.Time
}

// agentBand returns the script's open agent session, nil when there is none.
func (h *handler) agentBand(c *echo.Context, scriptID int64) (*agentBandView, error) {
	sess, err := h.Agent.Current(c.Request().Context(), scriptID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(sess.Problem), "\n")
	if r := []rune(first); len(r) > 120 {
		first = string(r[:120]) + "…"
	}
	return &agentBandView{Script: scriptID, ID: sess.ID, Agent: sess.Agent, Problem: first, Saves: sess.Saves, ExpiresAt: sess.ExpiresAt.Time}, nil
}

// ---- the hand-off form ------------------------------------------------------

type agentFormView struct {
	S       scripts.Script
	BasedOn int
	Hidden  string // the parameters whose defaults the agent reads masked
	FormID  string

	// as typed
	Problem string
	Agent   string
	For     string // minutes: 30 120 480
	Diff    bool

	Error string // translated
}

func (h *handler) renderAgentForm(c *echo.Context, status int, s scripts.Script, v agentFormView) error {
	ctx := c.Request().Context()
	d, err := h.Scripts.Draft(ctx, s.ID)
	if errors.Is(err, scripts.ErrNoDraft) {
		return web.Redirect(c, ScriptHref(s.ID))
	}
	if err != nil {
		return err
	}
	var hidden []string
	for _, p := range params.Parse([]byte(d.Body)).Params {
		if agent.Hidden(p) {
			hidden = append(hidden, p.Name)
		}
	}
	v.S, v.BasedOn, v.Hidden, v.FormID = s, d.BasedOn, strings.Join(hidden, ", "), agentapi.NewFormID()
	return web.Render(c, status, agentFormPage(h.shell(c, i18n.T(ctx, "agent.title"), ListPath), v))
}

func (h *handler) agentForm(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	return h.renderAgentForm(c, http.StatusOK, s, agentFormView{Agent: "claude-code", For: "120", Diff: true})
}

// ---- opening ----------------------------------------------------------------

type agentResultView struct {
	S         scripts.Script
	Masked    string
	Prompt    string // the real one, for the copy button
	Size      string
	ExpiresAt time.Time
	Repeated  bool // the form was posted before: the prompt is not shown again
}

func (h *handler) agentOpen(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	title := i18n.T(ctx, "agent.title")
	if fid := c.FormValue("form_id"); fid != "" && !h.forms.Take(fid) {
		return web.Render(c, http.StatusOK, agentResultPage(h.shell(c, title, ListPath), agentResultView{S: s, Repeated: true}))
	}
	v := agentFormView{Problem: c.FormValue("problem"), Agent: c.FormValue("agent"), For: c.FormValue("for"), Diff: c.FormValue("diff") == "1"}
	opened, err := h.Agent.OpenSession(ctx, agent.Open{ScriptID: s.ID, Problem: v.Problem, Agent: v.Agent, For: agentapi.FormDuration(v.For), IncludeDiff: v.Diff}, web.Actor(ctx))
	switch {
	case errors.Is(err, agent.ErrNoProblem):
		v.Error = i18n.T(ctx, "agent.err.problem")
	case errors.Is(err, agent.ErrBadAgent):
		v.Error = i18n.T(ctx, "agent.err.agent")
	case errors.Is(err, agent.ErrNoDraft):
		return web.Redirect(c, ScriptHref(s.ID))
	case err != nil:
		return notFoundOr(err)
	}
	if v.Error != "" {
		return h.renderAgentForm(c, http.StatusUnprocessableEntity, s, v)
	}
	// Whatever the browser does with this answer, the prompt is not cached.
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, agentResultPage(h.shell(c, title, ListPath), agentResultView{
		S: s, Masked: agentapi.Mask(opened.Prompt, opened.Token), Prompt: opened.Prompt, ExpiresAt: opened.ExpiresAt,
		Size: fmt.Sprintf("%.1f KB", float64(len(opened.Prompt))/1024),
	}))
}

// ---- revoke, polling --------------------------------------------------------

func (h *handler) agentRevoke(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	sid, err := strconv.ParseInt(c.Param("sid"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	// A session that already ended is as revoked as it can be.
	err = h.Agent.Revoke(c.Request().Context(), s.ID, sid, web.Actor(c.Request().Context()))
	switch {
	case errors.Is(err, agent.ErrNotFound):
		return echo.ErrNotFound
	case err != nil && !errors.Is(err, agent.ErrNotOpen):
		return err
	}
	return web.Redirect(c, ScriptHref(s.ID)+"?revoked=1#draft")
}

// draftRevision answers the editor's poll while an agent session is open: the
// same poller when the draft is as the editor holds it, the "draft changed"
// band when it is not. The editor sends the revision it holds as ?revision=.
func (h *handler) draftRevision(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	have, _ := strconv.Atoi(c.QueryParam("revision"))
	d, err := h.Scripts.Draft(ctx, s.ID)
	if err != nil {
		// No draft any more (published, discarded): nothing to watch.
		return web.Render(c, http.StatusOK, templ.NopComponent)
	}
	if d.Revision != have {
		return web.Render(c, http.StatusOK, draftChangedBand(s.ID))
	}
	if _, err := h.Agent.Current(ctx, s.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return web.Render(c, http.StatusOK, templ.NopComponent) // the session ended: stop asking
		}
		return err
	}
	return web.Render(c, http.StatusOK, draftPoller(s.ID))
}

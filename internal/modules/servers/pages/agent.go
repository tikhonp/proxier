package pages

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/agent"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) registerAgent(r web.Routes) {
	a := r.Admin
	a.GET("/templates/:id/agent", h.agentForm)
	a.POST("/templates/:id/agent", h.agentOpen)
	a.POST("/templates/:id/agent/:sid/revoke", h.agentRevoke)
	a.GET("/templates/:id/draft/revision", h.draftRevision)
}

// usedForms remembers the hand-off forms that already opened a session. The
// prompt is shown once, in the answer to the post; reloading that answer posts
// the form again, which must not hand the draft over a second time.
type usedForms struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// take reports whether id is new, and remembers it.
func (u *usedForms) take(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.seen == nil {
		u.seen = map[string]time.Time{}
	}
	for k, at := range u.seen {
		if time.Since(at) > time.Hour {
			delete(u.seen, k)
		}
	}
	if _, dup := u.seen[id]; dup {
		return false
	}
	u.seen[id] = time.Now()
	return true
}

func newFormID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- the band ---------------------------------------------------------------

// agentBandView is the open session as the template page and the editor show it.
type agentBandView struct {
	Template    int64
	ID          int64
	Agent       string
	Problem     string // its first line
	Saves       int
	Validations int
	ExpiresAt   time.Time
}

// agentBand returns the template's open agent session, nil when there is none.
func (h *handler) agentBand(c *echo.Context, templateID int64) (*agentBandView, error) {
	sess, err := h.Agent.Current(c.Request().Context(), templateID)
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
	return &agentBandView{Template: templateID, ID: sess.ID, Agent: sess.Agent, Problem: first, Saves: sess.Saves,
		Validations: sess.Validations, ExpiresAt: sess.ExpiresAt.Time}, nil
}

// ---- the hand-off form ------------------------------------------------------

type agentFormView struct {
	T       templates.Info
	BasedOn int
	Servers []store.ServerRef
	Failed  []agentJob
	FormID  string

	// as typed
	Problem string
	Agent   string
	For     string // minutes: 30 120 480
	Report  bool
	Diff    bool
	Job     int64
	Picked  map[int64]bool

	Error string // translated
}

type agentJob struct {
	ID    int64
	Label string
}

// agentDurations are the access lengths the form offers (minutes).
var agentDurations = []string{"30", "120", "480"}

func (h *handler) renderAgentForm(c *echo.Context, status int, id int64, v agentFormView) error {
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	d, err := h.Templates.Draft(ctx, id)
	if err != nil {
		return redirectGone(c, id, err)
	}
	v.T, v.BasedOn = info, d.BasedOn
	if v.Servers, err = store.ActiveServersOf(ctx, h.Store.DB.R, id); err != nil {
		return err
	}
	failed, err := h.Agent.FailedJobs(ctx, id)
	if err != nil {
		return err
	}
	v.Failed = nil
	for _, f := range failed {
		label := i18n.T(ctx, "agent.ctx.job.item", i18n.Args{"server": f.ServerName, "id": f.ID, "step": f.Step})
		v.Failed = append(v.Failed, agentJob{ID: f.ID, Label: label})
	}
	v.FormID = newFormID()
	return web.Render(c, status, agentFormPage(h.shell(c, i18n.T(ctx, "agent.title"), "/templates"), v))
}

func (h *handler) agentForm(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	return h.renderAgentForm(c, http.StatusOK, id, agentFormView{Agent: "claude-code", For: "120", Report: true, Diff: true})
}

// ---- opening ----------------------------------------------------------------

type agentResultView struct {
	T         templates.Info
	Masked    string
	Prompt    string // the real one, for the copy button
	Size      string
	ExpiresAt time.Time
	Repeated  bool // the form was posted before: the prompt is not shown again
}

func (h *handler) agentOpen(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	if fid := c.FormValue("form_id"); fid != "" && !h.forms.take(fid) {
		return web.Render(c, http.StatusOK, agentResultPage(h.shell(c, i18n.T(ctx, "agent.title"), "/templates"), agentResultView{T: info, Repeated: true}))
	}
	v := agentFormView{
		Problem: c.FormValue("problem"), Agent: c.FormValue("agent"), For: c.FormValue("for"),
		Report: c.FormValue("report") == "1", Diff: c.FormValue("diff") == "1", Picked: map[int64]bool{},
	}
	o := agent.Open{TemplateID: id, Problem: v.Problem, Agent: v.Agent, IncludeReport: v.Report, IncludeDiff: v.Diff}
	mins, _ := strconv.Atoi(v.For)
	if !containsStr(agentDurations, v.For) {
		mins = 120
	}
	o.For = time.Duration(mins) * time.Minute
	if raw := c.FormValue("job"); raw != "" {
		o.JobID, _ = strconv.ParseInt(raw, 10, 64)
		v.Job = o.JobID
	}
	for _, raw := range c.Request().Form["server"] {
		if sid, err := strconv.ParseInt(raw, 10, 64); err == nil {
			o.ServerIDs = append(o.ServerIDs, sid)
			v.Picked[sid] = true
		}
	}
	opened, err := h.Agent.OpenSession(ctx, o, "admin")
	if err != nil {
		key := ""
		switch {
		case errors.Is(err, agent.ErrNoProblem):
			key = "agent.err.problem"
		case errors.Is(err, agent.ErrBadAgent):
			key = "agent.err.agent"
		case errors.Is(err, agent.ErrBadJob):
			key = "agent.err.job"
		case errors.Is(err, agent.ErrBadServer):
			key = "agent.err.server"
		case errors.Is(err, agent.ErrNoDraft):
			return redirectGone(c, id, err)
		}
		if key == "" {
			return notFound(err)
		}
		v.Error = i18n.T(ctx, key)
		return h.renderAgentForm(c, http.StatusUnprocessableEntity, id, v)
	}
	// Whatever the browser does with this answer, the prompt is not cached.
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, agentResultPage(h.shell(c, i18n.T(ctx, "agent.title"), "/templates"), agentResultView{
		T: info, Masked: agent.Mask(opened.Prompt, opened.Token), Prompt: opened.Prompt, ExpiresAt: opened.ExpiresAt,
		Size: fmt.Sprintf("%.1f KB", float64(len(opened.Prompt))/1024),
	}))
}

func containsStr(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// ---- revoke, polling --------------------------------------------------------

func (h *handler) agentRevoke(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	sid, err := strconv.ParseInt(c.Param("sid"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	sess, err := store.AgentSessionByID(c.Request().Context(), h.Store.DB.R, sid)
	if err != nil || sess.TemplateID != id {
		return echo.ErrNotFound
	}
	// A session that already ended is as revoked as it can be.
	if err := h.Agent.Revoke(c.Request().Context(), sid, "admin"); err != nil && !errors.Is(err, agent.ErrNotOpen) {
		return notFound(err)
	}
	return web.Redirect(c, actionHref(id, "?saved=revoked"))
}

// draftRevision answers the editor's poll while an agent session is open: the
// same poller when the draft is as the editor holds it, the "draft changed"
// band when it is not. The editor sends the revision it holds as ?revision=.
func (h *handler) draftRevision(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	have, _ := strconv.Atoi(c.QueryParam("revision"))
	d, err := h.Templates.Draft(ctx, id)
	if err != nil {
		// No draft any more (published, discarded): nothing to watch.
		return web.Render(c, http.StatusOK, templ.NopComponent)
	}
	if d.Revision != have {
		return web.Render(c, http.StatusOK, draftChangedBand(id))
	}
	if _, err := h.Agent.Current(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return web.Render(c, http.StatusOK, templ.NopComponent) // the session ended: stop asking
		}
		return err
	}
	return web.Render(c, http.StatusOK, draftPoller(id))
}

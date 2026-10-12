package agent

import (
	_ "embed"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/agentapi"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// ParameterDocs is the reference served at /docs/parameters: a copy of the
// Parameters section of docs/modules/router-scripts.md, kept equal by a test.
//
//go:embed parameters.md
var ParameterDocs string

// APIPrefix is where the API lives; the template agent API has the rest of /agent/v1.
const APIPrefix = "/agent/v1/router-script"

const (
	sessionKey = "agentSession"
	// maxBody is the largest request body: one script, plus room for the
	// line endings Normalize may take away.
	maxBody = 2 * scripts.MaxBody
)

var errSessionEnded = errors.New(agentapi.SessionEnded)

// Routes adds the agent API to g, which must be the public group: the API never
// reads or sets a cookie and answers to a bearer token only.
func (s *Service) Routes(g *echo.Group) {
	a := g.Group(APIPrefix, s.auth)
	a.GET("/context", s.getContext)
	a.GET("/draft", s.getDraft)
	a.PUT("/draft", s.putDraft)
	a.POST("/draft/check", s.checkDraft)
	a.GET("/docs/parameters", s.docsParameters)
}

// auth finds the session of the bearer token and counts the request against its
// limits in one short write. Every refusal to authenticate is the same 401: an
// ended, expired and unknown token cannot be told apart.
func (s *Service) auth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		r := c.Request()
		tok := agentapi.Bearer(r)
		if tok == "" {
			return agentapi.Ended(c)
		}
		if r.ContentLength > maxBody {
			return agentapi.Fail(c, http.StatusRequestEntityTooLarge, "the request body is too large")
		}
		r.Body = http.MaxBytesReader(c.Response(), r.Body, maxBody)

		var sess store.AgentSession
		var status int
		var retry time.Duration
		now := s.now()
		err := s.DB.Write(r.Context(), func(tx *sqlx.Tx) error {
			var err error
			sess, err = store.AgentSessionByToken(r.Context(), tx, s.Vault.Lookup(tok))
			if errors.Is(err, store.ErrNotFound) {
				status = http.StatusUnauthorized
				return nil
			}
			if err != nil {
				return err
			}
			if !sess.Open() || !sess.ExpiresAt.After(now.Time) {
				status = http.StatusUnauthorized
				return nil
			}
			rate, wait, ok := agentapi.Rate{Requests: sess.Requests, MinuteStart: sess.MinuteStart, MinuteCount: sess.MinuteCount}.Count(now, sess.ExpiresAt)
			if !ok {
				status, retry = http.StatusTooManyRequests, wait
				return nil
			}
			sess.Requests = rate.Requests
			return store.SetAgentRate(r.Context(), tx, sess.ID, rate.Requests, rate.MinuteStart, rate.MinuteCount)
		})
		if err != nil {
			s.Log.Error("rscripts agent: counting a request", "error", err)
			return agentapi.Fail(c, http.StatusInternalServerError, "internal error")
		}
		switch status {
		case http.StatusUnauthorized:
			return agentapi.Ended(c)
		case http.StatusTooManyRequests:
			return agentapi.TooMany(c, retry)
		}
		c.Set(sessionKey, sess)
		return next(c)
	}
}

func session(c *echo.Context) store.AgentSession { return c.Get(sessionKey).(store.AgentSession) }

// stillOpen runs in a write transaction: it refuses (errSessionEnded) when the
// session was revoked, replaced or ran out since the request was let in, so an
// ended token never changes the draft.
func (s *Service) stillOpen(c *echo.Context, tx *sqlx.Tx, sess store.AgentSession) error {
	cur, err := store.AgentSessionByID(c.Request().Context(), tx, sess.ID)
	if err != nil {
		return err
	}
	if !cur.Open() || !cur.ExpiresAt.After(s.Now()) {
		return errSessionEnded
	}
	return nil
}

func etag(revision int) string { return `"` + strconv.Itoa(revision) + `"` }

func noDraft(c *echo.Context) error {
	return agentapi.Fail(c, http.StatusConflict, "the script has no draft")
}

// ---- the check --------------------------------------------------------------

type findingJSON struct {
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type paramJSON struct {
	Name        string   `json:"name"`
	Line        int      `json:"line"`
	Default     string   `json:"default"`
	Hidden      bool     `json:"hidden,omitempty"`
	Description string   `json:"description,omitempty"`
	Annotations []string `json:"annotations,omitempty"`
}

type computedJSON struct {
	Name  string `json:"name"`
	Line  int    `json:"line"`
	Expr  string `json:"expr"`
	Value string `json:"value,omitempty"`
}

// checkJSON is what Publish would say about a body, and what it reads from it.
type checkJSON struct {
	OK         bool           `json:"ok"`
	Errors     int            `json:"errors"`
	Warnings   int            `json:"warnings"`
	Findings   []findingJSON  `json:"findings"`
	Block      bool           `json:"parameters_block"`
	Ended      bool           `json:"end_marker"`
	Parameters []paramJSON    `json:"parameters"`
	Computed   []computedJSON `json:"computed"`
}

// check reports on body as Publish would. The findings come from the real body
// and hold no values; the parameters and computed values are read from the
// masked one, so a value derived from a hidden default stays hidden.
func (s *Service) check(c *echo.Context, scriptID int64, body string, basedOn int) (checkJSON, error) {
	_, fs, err := s.Scripts.ReportFor(c.Request().Context(), scriptID, body, basedOn)
	if err != nil {
		return checkJSON{}, err
	}
	shown := params.Parse([]byte(maskBody(body)))
	loc := s.I18n.Localizer(i18n.EN, nil)
	out := checkJSON{Block: shown.Block, Ended: shown.Ended, Findings: []findingJSON{}, Parameters: []paramJSON{}, Computed: []computedJSON{}}
	for _, f := range fs {
		out.Findings = append(out.Findings, findingJSON{Line: f.Line, Severity: f.Severity, Message: loc.T(f.Key, plain(f.Args))})
		if f.Severity == params.Error {
			out.Errors++
		} else {
			out.Warnings++
		}
	}
	out.OK = out.Errors == 0
	for _, p := range shown.Params {
		out.Parameters = append(out.Parameters, paramJSON{Name: p.Name, Line: p.Line, Default: p.Default, Hidden: p.Default == Masked && Hidden(p),
			Description: p.Description, Annotations: annotations(p.Annotations)})
	}
	vals := params.Eval(shown, nil)
	for _, cv := range shown.Computed {
		out.Computed = append(out.Computed, computedJSON{Name: cv.Name, Line: cv.Line, Expr: cv.Expr, Value: vals[cv.Name]})
	}
	return out, nil
}

// plain writes numbers as they are: line 1234 must not become "1,234".
func plain(a map[string]any) i18n.Args {
	out := i18n.Args{}
	for k, v := range a {
		if n, ok := v.(int); ok {
			out[k] = strconv.Itoa(n)
			continue
		}
		out[k] = v
	}
	return out
}

func annotations(a params.Annotations) []string {
	var out []string
	if a.Secret {
		out = append(out, "@secret")
	}
	if a.Required {
		out = append(out, "@required")
	}
	if len(a.Choices) > 0 {
		out = append(out, "@choices "+strings.Join(a.Choices, "|"))
	}
	if a.Pattern != "" {
		out = append(out, "@pattern "+a.Pattern)
	}
	if a.Fill != "" {
		out = append(out, "@fill "+a.Fill)
	}
	return out
}

// ---- reading ----------------------------------------------------------------

type contextJSON struct {
	Problem string `json:"problem"`
	Agent   string `json:"agent"`
	Script  struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		BaseVersion int    `json:"base_version"`
		Revision    int    `json:"revision"`
	} `json:"script"`
	ExpiresAt time.Time `json:"expires_at"`
	Masked    string    `json:"masked_value"`
	Check     checkJSON `json:"check"`
	Diff      *string   `json:"diff"` // null unless the owner included it
	Links     struct {
		Draft      string `json:"draft"`
		Check      string `json:"check"`
		Parameters string `json:"docs_parameters"`
	} `json:"links"`
}

func (s *Service) getContext(c *echo.Context) error {
	ctx := c.Request().Context()
	sess := session(c)
	sc, err := s.Scripts.Get(ctx, sess.ScriptID)
	if err != nil {
		return agentapi.Ended(c)
	}
	d, err := s.Scripts.Draft(ctx, sess.ScriptID)
	if err != nil {
		return noDraft(c)
	}
	out := contextJSON{Problem: sess.Problem, Agent: sess.Agent, ExpiresAt: sess.ExpiresAt.Time, Masked: Masked}
	out.Script.Name, out.Script.Slug, out.Script.BaseVersion, out.Script.Revision = sc.Name, sc.Slug, d.BasedOn, d.Revision
	out.Links.Draft, out.Links.Check, out.Links.Parameters = APIPrefix+"/draft", APIPrefix+"/draft/check", APIPrefix+"/docs/parameters"
	if out.Check, err = s.check(c, sess.ScriptID, d.Body, d.BasedOn); err != nil {
		return err
	}
	if ContextOf(sess).Diff && d.BasedOn > 0 {
		base, err := s.Scripts.Version(ctx, sess.ScriptID, d.BasedOn)
		if err != nil {
			return err
		}
		diff := ""
		if a, b := maskBody(base.Body), maskBody(d.Body); a != b {
			diff = scripts.Unified(sc.Slug, strconv.Itoa(d.BasedOn), scripts.DraftRef, a, b)
		}
		out.Diff = &diff
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Service) getDraft(c *echo.Context) error {
	d, err := s.Scripts.Draft(c.Request().Context(), session(c).ScriptID)
	if err != nil {
		return noDraft(c)
	}
	c.Response().Header().Set("ETag", etag(d.Revision))
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", []byte(maskBody(d.Body)))
}

// readBody reads the request's script with the stored draft's line endings
// and its hidden defaults put back. code is 0 when it worked.
func readBody(c *echo.Context, stored string) (string, int, string) {
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return "", http.StatusRequestEntityTooLarge, "the request body is too large"
		}
		return "", http.StatusBadRequest, "could not read the body"
	}
	body, err := unmask(scripts.Normalize(string(raw), stored), stored)
	if err != nil {
		return "", http.StatusUnprocessableEntity, err.Error() + ": keep " + Masked + " only where the draft you read had it, and keep the parameter's name and quotes"
	}
	return body, 0, ""
}

// ---- writing ----------------------------------------------------------------

func (s *Service) putDraft(c *echo.Context) error {
	ctx := c.Request().Context()
	sess := session(c)
	d, err := s.Scripts.Draft(ctx, sess.ScriptID)
	if err != nil {
		return noDraft(c)
	}
	switch m := c.Request().Header.Get("If-Match"); {
	case m == "":
		return agentapi.Fail(c, http.StatusPreconditionRequired, "send If-Match with the draft's ETag")
	case !agentapi.Matches(m, etag(d.Revision)):
		return agentapi.Fail(c, http.StatusPreconditionFailed, "the draft changed")
	}
	body, code, msg := readBody(c, d.Body)
	if code != 0 {
		return agentapi.Fail(c, code, msg)
	}
	changed := false
	rev, err := s.Scripts.SaveDraftIn(ctx, sess.ScriptID, d.Revision, body, events.AgentActor(sess.ID), func(tx *sqlx.Tx, ch bool) error {
		if err := s.stillOpen(c, tx, sess); err != nil {
			return err
		}
		changed = ch
		if ch {
			return store.AddAgentSave(ctx, tx, sess.ID)
		}
		return nil
	})
	var fe store.FieldErrors
	switch {
	case errors.Is(err, scripts.ErrDraftChanged):
		return agentapi.Fail(c, http.StatusPreconditionFailed, "the draft changed")
	case errors.Is(err, errSessionEnded):
		return agentapi.Ended(c)
	case errors.Is(err, scripts.ErrNoDraft):
		return noDraft(c)
	case errors.As(err, &fe):
		return agentapi.Fail(c, http.StatusUnprocessableEntity, s.I18n.Localizer(i18n.EN, nil).T(fe["body"]))
	case err != nil:
		s.Log.Error("rscripts agent: saving the draft", "error", err)
		return agentapi.Fail(c, http.StatusInternalServerError, "internal error")
	}
	report, err := s.check(c, sess.ScriptID, body, d.BasedOn)
	if err != nil {
		return err
	}
	c.Response().Header().Set("ETag", etag(rev))
	return c.JSON(http.StatusOK, map[string]any{"revision": rev, "etag": etag(rev), "changed": changed, "check": report})
}

// checkDraft checks the saved draft, or the script in the request body
// without saving it.
func (s *Service) checkDraft(c *echo.Context) error {
	ctx := c.Request().Context()
	sess := session(c)
	d, err := s.Scripts.Draft(ctx, sess.ScriptID)
	if err != nil {
		return noDraft(c)
	}
	body, code, msg := readBody(c, d.Body)
	if code != 0 {
		return agentapi.Fail(c, code, msg)
	}
	saved := body == ""
	if saved {
		body = d.Body
	}
	report, err := s.check(c, sess.ScriptID, body, d.BasedOn)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"revision": d.Revision, "saved": saved, "check": report})
}

func (s *Service) docsParameters(c *echo.Context) error {
	return c.Blob(http.StatusOK, "text/markdown; charset=utf-8", []byte(ParameterDocs))
}

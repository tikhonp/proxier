package agent

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// ManifestDocs is the manifest reference served at /docs/manifest: a copy of
// the manifest and rendering sections of docs/modules/servers.md, kept equal by
// a test.
//
//go:embed manifest.md
var ManifestDocs string

const (
	apiPrefix  = "/agent/v1"
	sessionKey = "agent.session"
	// maxBody is the largest request body: one file.
	maxBody = templates.MaxFileBytes
)

var errSessionEnded = errors.New("session ended")

// Routes adds the agent API to g, which must be the public group: the API never
// reads or sets a cookie and answers to a bearer token only.
func (s *Service) Routes(g *echo.Group) {
	a := g.Group(apiPrefix, s.auth)
	a.GET("/context", s.getContext)
	a.GET("/draft", s.getDraft)
	a.GET("/draft/files/*", s.getFile)
	a.PUT("/draft/files/*", s.putFile)
	a.DELETE("/draft/files/*", s.deleteFile)
	a.POST("/draft/validate", s.validateDraft)
	a.POST("/draft/preview", s.previewDraft)
	a.GET("/docs/manifest", s.docsManifest)
}

// ---- auth and limits --------------------------------------------------------

func fail(c *echo.Context, code int, msg string) error {
	return c.JSON(code, map[string]string{"error": msg})
}

func ended(c *echo.Context) error {
	c.Response().Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	return fail(c, http.StatusUnauthorized, errSessionEnded.Error())
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// auth finds the session of the bearer token and counts the request against its
// limits in one short write. Every refusal to authenticate is the same 401: an
// ended, expired and unknown token cannot be told apart.
func (s *Service) auth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		r := c.Request()
		tok := bearer(r)
		if !strings.HasPrefix(tok, TokenPrefix) || len(tok) > 128 {
			return ended(c)
		}
		if r.ContentLength > maxBody {
			return fail(c, http.StatusRequestEntityTooLarge, "the request body is too large")
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
			if sess.Requests >= MaxRequests {
				status, retry = http.StatusTooManyRequests, sess.ExpiresAt.Sub(now.Time)
				return nil
			}
			start, count := sess.MinuteStart, sess.MinuteCount
			if start.IsZero() || !now.Before(start.Add(time.Minute)) {
				start, count = now, 0
			}
			if count >= MaxPerMinute {
				status, retry = http.StatusTooManyRequests, start.Add(time.Minute).Sub(now.Time)
				return nil
			}
			sess.Requests++
			return store.SetAgentRate(r.Context(), tx, sess.ID, sess.Requests, start, count+1)
		})
		if err != nil {
			s.Log.Error("agent: counting a request", "error", err)
			return fail(c, http.StatusInternalServerError, "internal error")
		}
		switch status {
		case http.StatusUnauthorized:
			return ended(c)
		case http.StatusTooManyRequests:
			c.Response().Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			return fail(c, status, "too many requests")
		}
		c.Set(sessionKey, sess)
		return next(c)
	}
}

func session(c *echo.Context) store.AgentSession { return c.Get(sessionKey).(store.AgentSession) }

func (s *Service) actor(sess store.AgentSession) string { return events.AgentActor(sess.ID) }

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

// ---- ETags ------------------------------------------------------------------

func fileETag(revision int, content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf(`"%d-%x"`, revision, sum[:8])
}

func draftETag(revision int) string { return fmt.Sprintf(`"%d"`, revision) }

// matches reports whether a conditional header value names etag.
func matches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(p), "W/")
		if p == etag {
			return true
		}
	}
	return false
}

// ---- reading ----------------------------------------------------------------

type linkJSON struct {
	Draft    string `json:"draft"`
	Manifest string `json:"docs_manifest"`
	Validate string `json:"validate"`
}

type diffJSON struct {
	Path    string `json:"path"`
	Change  string `json:"change"`
	Binary  bool   `json:"binary,omitempty"`
	Unified string `json:"unified,omitempty"`
}

type contextJSON struct {
	Problem  string `json:"problem"`
	Agent    string `json:"agent"`
	Template struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		BaseVersion int    `json:"base_version"`
		Revision    int    `json:"revision"`
	} `json:"template"`
	ExpiresAt time.Time       `json:"expires_at"`
	Report    *ReportSnapshot `json:"report,omitempty"`
	Job       *JobSnapshot    `json:"failed_job,omitempty"`
	Previews  []string        `json:"previews,omitempty"`
	Diff      []diffJSON      `json:"diff"` // null unless the owner included it
	Links     linkJSON        `json:"links"`
}

func (s *Service) getContext(c *echo.Context) error {
	ctx := c.Request().Context()
	sess := session(c)
	t, err := store.GetTemplate(ctx, s.DB.R, sess.TemplateID)
	if err != nil {
		return ended(c)
	}
	d, err := s.Templates.Draft(ctx, sess.TemplateID)
	if err != nil {
		return fail(c, http.StatusConflict, "the template has no draft")
	}
	cx := ContextOf(sess)
	out := contextJSON{Problem: sess.Problem, Agent: sess.Agent, ExpiresAt: sess.ExpiresAt.Time, Report: cx.Report, Job: cx.Job,
		Links: linkJSON{Draft: apiPrefix + "/draft", Manifest: apiPrefix + "/docs/manifest", Validate: apiPrefix + "/draft/validate"}}
	out.Template.Name, out.Template.Slug, out.Template.BaseVersion, out.Template.Revision = t.Name, t.Slug, d.BasedOn, d.Revision
	for _, sv := range cx.Servers {
		out.Previews = append(out.Previews, sv.Name)
	}
	if cx.Diff {
		base := map[string][]byte{}
		if d.BasedOn > 0 {
			v, err := s.Templates.Version(ctx, sess.TemplateID, d.BasedOn)
			if err != nil {
				return err
			}
			base = v.Files
		}
		out.Diff = []diffJSON{}
		for _, f := range templates.Diff(base, d.Files) {
			if f.Change != templates.Unchanged {
				out.Diff = append(out.Diff, diffJSON{Path: f.Path, Change: f.Change, Binary: f.Binary, Unified: f.Unified})
			}
		}
	}
	return c.JSON(http.StatusOK, out)
}

type fileJSON struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	ETag string `json:"etag"`
}

func (s *Service) getDraft(c *echo.Context) error {
	d, err := s.Templates.Draft(c.Request().Context(), session(c).TemplateID)
	if err != nil {
		return fail(c, http.StatusConflict, "the template has no draft")
	}
	files := make([]fileJSON, 0, len(d.Files))
	for _, p := range sortedKeys(d.Files) {
		files = append(files, fileJSON{Path: p, Size: len(d.Files[p]), ETag: fileETag(d.Revision, d.Files[p])})
	}
	c.Response().Header().Set("ETag", draftETag(d.Revision))
	return c.JSON(http.StatusOK, map[string]any{
		"revision": d.Revision, "etag": draftETag(d.Revision), "based_on": d.BasedOn,
		"manifest": string(d.Files[manifest.Name]), "files": files,
	})
}

func (s *Service) getFile(c *echo.Context) error {
	p := c.Param("*")
	d, err := s.Templates.Draft(c.Request().Context(), session(c).TemplateID)
	if err != nil {
		return fail(c, http.StatusConflict, "the template has no draft")
	}
	content, ok := d.Files[p]
	if !ok || !templates.ValidPath(p) {
		return fail(c, http.StatusNotFound, "no such file")
	}
	h := c.Response().Header()
	h.Set("ETag", fileETag(d.Revision, content))
	ct := "text/plain; charset=utf-8"
	if !utf8.Valid(content) {
		ct = "application/octet-stream"
	}
	return c.Blob(http.StatusOK, ct, content)
}

// ---- writing ----------------------------------------------------------------

// precondition checks the conditional headers of a write against the file as
// it is now. A missing header is 428, a wrong one 412.
func precondition(c *echo.Context, exists bool, etag string) (int, string) {
	r := c.Request()
	ifMatch, ifNone := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if exists {
		switch {
		case ifMatch == "":
			return http.StatusPreconditionRequired, "send If-Match with the file's ETag"
		case !matches(ifMatch, etag):
			return http.StatusPreconditionFailed, "the draft changed"
		}
		return 0, ""
	}
	switch {
	case ifMatch != "":
		return http.StatusPreconditionFailed, "the draft changed"
	case ifNone != "*":
		return http.StatusPreconditionRequired, "the file does not exist: send If-None-Match: * to create it"
	}
	return 0, ""
}

func total(files map[string][]byte) int {
	n := 0
	for _, b := range files {
		n += len(b)
	}
	return n
}

func (s *Service) putFile(c *echo.Context) error {
	p := c.Param("*")
	if !templates.ValidPath(p) {
		return fail(c, http.StatusBadRequest, "bad path")
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return fail(c, http.StatusRequestEntityTooLarge, "the file is over 1 MiB")
		}
		return fail(c, http.StatusBadRequest, "could not read the body")
	}
	return s.write(c, p, body, false)
}

func (s *Service) deleteFile(c *echo.Context) error {
	p := c.Param("*")
	if !templates.ValidPath(p) {
		return fail(c, http.StatusBadRequest, "bad path")
	}
	return s.write(c, p, nil, true)
}

// write changes one file of the draft: put it, or remove it.
func (s *Service) write(c *echo.Context, p string, body []byte, remove bool) error {
	ctx := c.Request().Context()
	sess := session(c)
	d, err := s.Templates.Draft(ctx, sess.TemplateID)
	if err != nil {
		return fail(c, http.StatusConflict, "the template has no draft")
	}
	old, exists := d.Files[p]
	if remove && !exists {
		return fail(c, http.StatusNotFound, "no such file")
	}
	if code, msg := precondition(c, exists, fileETag(d.Revision, old)); code != 0 {
		return fail(c, code, msg)
	}
	files := make(map[string][]byte, len(d.Files)+1)
	for k, v := range d.Files {
		files[k] = v
	}
	if remove {
		delete(files, p)
	} else {
		if !exists && len(files) >= templates.MaxImportFiles {
			return fail(c, http.StatusRequestEntityTooLarge, "the draft already has 500 files")
		}
		files[p] = body
		if total(files) > templates.MaxDraftBytes {
			return fail(c, http.StatusRequestEntityTooLarge, "the draft would be over 10 MiB")
		}
	}
	rev, err := s.Templates.SaveDraftIn(ctx, sess.TemplateID, d.Revision, files, "agent", s.actor(sess), func(tx *sqlx.Tx, changed bool) error {
		if err := s.stillOpen(c, tx, sess); err != nil {
			return err
		}
		if changed {
			return store.AddAgentSave(ctx, tx, sess.ID)
		}
		return nil
	})
	switch {
	case errors.Is(err, templates.ErrDraftChanged):
		return fail(c, http.StatusPreconditionFailed, "the draft changed")
	case errors.Is(err, errSessionEnded):
		return ended(c)
	case errors.Is(err, templates.ErrNoDraft):
		return fail(c, http.StatusConflict, "the template has no draft")
	case err != nil:
		s.Log.Error("agent: saving a file", "error", err)
		return fail(c, http.StatusInternalServerError, "internal error")
	}
	h := c.Response().Header()
	out := map[string]any{"revision": rev, "path": p}
	status := http.StatusOK
	switch {
	case remove:
		out["deleted"] = true
	default:
		out["etag"] = fileETag(rev, body)
		h.Set("ETag", fileETag(rev, body))
		if !exists {
			status = http.StatusCreated
		}
	}
	return c.JSON(status, out)
}

// ---- validate, preview, docs ------------------------------------------------

type findingsJSON struct {
	OK       bool              `json:"ok"`
	Errors   int               `json:"errors"`
	Warnings int               `json:"warnings"`
	Findings []finding.Finding `json:"findings"`
	Revision int               `json:"revision"`
}

func (s *Service) validateDraft(c *echo.Context) error {
	ctx := c.Request().Context()
	sess := session(c)
	report, err := s.Templates.ValidateDraftIn(ctx, sess.TemplateID, "agent", s.actor(sess), func(tx *sqlx.Tx) error {
		if err := s.stillOpen(c, tx, sess); err != nil {
			return err
		}
		return store.AddAgentValidation(ctx, tx, sess.ID)
	})
	switch {
	case errors.Is(err, errSessionEnded):
		return ended(c)
	case errors.Is(err, templates.ErrNoDraft):
		return fail(c, http.StatusConflict, "the template has no draft")
	case err != nil:
		s.Log.Error("agent: validating", "error", err)
		return fail(c, http.StatusInternalServerError, "internal error")
	}
	d, err := s.Templates.Draft(ctx, sess.TemplateID)
	if err != nil {
		return fail(c, http.StatusConflict, "the template has no draft")
	}
	fs := report.Findings
	if fs == nil {
		fs = []finding.Finding{}
	}
	return c.JSON(http.StatusOK, findingsJSON{OK: report.OK(), Errors: len(report.Errors()), Warnings: len(report.Warnings()), Findings: fs, Revision: d.Revision})
}

type previewFileJSON struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Binary  bool   `json:"binary,omitempty"`
	Size    int    `json:"size"`
	Content string `json:"content,omitempty"`
}

func (s *Service) previewDraft(c *echo.Context) error {
	ctx := c.Request().Context()
	sess := session(c)
	name := c.QueryParam("server")
	allowed := false
	var id int64
	for _, sv := range ContextOf(sess).Servers {
		if sv.Name == name {
			allowed, id = true, sv.ID
		}
	}
	if !allowed {
		return fail(c, http.StatusForbidden, "this session may not preview that server")
	}
	// Still an active server of the template? It may have been retired since.
	if _, err := store.ServerOfTemplateByName(ctx, s.DB.R, sess.TemplateID, name); err != nil {
		return fail(c, http.StatusConflict, "that server is no longer active")
	}
	d, err := s.Templates.Draft(ctx, sess.TemplateID)
	if err != nil {
		return fail(c, http.StatusConflict, "the template has no draft")
	}
	build, err := s.Preview(ctx, id)
	if err != nil {
		s.Log.Error("agent: preview context", "error", err)
		return fail(c, http.StatusInternalServerError, "internal error")
	}
	pv, err := s.Templates.PreviewFor(ctx, sess.TemplateID, d.Files, build)
	if err != nil {
		s.Log.Error("agent: preview", "error", err)
		return fail(c, http.StatusInternalServerError, "internal error")
	}
	files := make([]previewFileJSON, 0, len(pv.Files))
	for _, f := range pv.Files {
		pf := previewFileJSON{Path: f.Path, Mode: strconv.FormatUint(uint64(f.Mode.Perm()), 8), Size: len(f.Content)}
		if utf8.Valid(f.Content) {
			pf.Content = string(f.Content)
		} else {
			pf.Binary = true
		}
		files = append(files, pf)
	}
	fs := pv.Findings
	if fs == nil {
		fs = []finding.Finding{}
	}
	c.Response().Header().Set("ETag", draftETag(d.Revision))
	return c.JSON(http.StatusOK, map[string]any{"server": name, "revision": d.Revision, "files": files, "findings": fs, "masked": true})
}

func (s *Service) docsManifest(c *echo.Context) error {
	return c.Blob(http.StatusOK, "text/markdown; charset=utf-8", []byte(ManifestDocs))
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

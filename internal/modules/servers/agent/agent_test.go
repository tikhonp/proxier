package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/agent"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var bg = context.Background()

// env is a harness whose agent service runs on a clock the test moves, with
// the seed template's draft open.
type env struct {
	T   *testing.T
	H   *serverstest.Harness
	Ag  *agent.Service
	Now time.Time
	Tpl int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	e := &env{T: t, H: h, Ag: h.Mod.Agent, Now: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC), Tpl: h.TemplateID}
	e.Ag.Now = func() time.Time { return e.Now }
	if _, err := h.Mod.Templates.EditDraft(bg, e.Tpl, "admin"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) advance(d time.Duration) { e.Now = e.Now.Add(d) }

// open hands the draft over with a default problem unless o names one.
func (e *env) open(o agent.Open) agent.Opened {
	e.T.Helper()
	if o.TemplateID == 0 {
		o.TemplateID = e.Tpl
	}
	if o.Problem == "" {
		o.Problem = "nginx will not start; fix the draft"
	}
	if o.Agent == "" {
		o.Agent = "claude-code"
	}
	op, err := e.Ag.OpenSession(bg, o, "admin")
	if err != nil {
		e.T.Fatalf("open session: %v", err)
	}
	return op
}

// req is one call to the agent API.
type req struct {
	Method, Path string
	Token        string
	Header       map[string]string
	Body         []byte
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.T.Helper()
	if r.Method == "" {
		r.Method = http.MethodGet
	}
	h := http.Header{}
	if r.Token != "" {
		h.Set("Authorization", "Bearer "+r.Token)
	}
	for k, v := range r.Header {
		h.Set(k, v)
	}
	sr := sitetest.Req{Method: r.Method, Path: "/agent/v1" + r.Path, Header: h}
	if r.Body != nil {
		sr.Body, sr.ContentType = r.Body, "application/octet-stream"
	}
	return e.H.Site.Do(sr)
}

func (e *env) get(token, path string) *httptest.ResponseRecorder {
	e.T.Helper()
	return e.do(req{Path: path, Token: token})
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, rec.Body)
	}
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d\n%s", rec.Code, want, rec.Body)
	}
}

// ended asserts the answer of a token that no longer works.
func ended(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	status(t, rec, http.StatusUnauthorized)
	if !strings.Contains(rec.Body.String(), "session ended") {
		t.Fatalf("401 without \"session ended\": %s", rec.Body)
	}
}

// closed returns the payloads of template.agent_session_closed, newest first.
func (e *env) closed() []map[string]any {
	e.T.Helper()
	var out []map[string]any
	for _, ev := range e.H.Events("template.agent_session_closed") {
		out = append(out, ev.Payload)
	}
	return out
}

func (e *env) session(id int64) store.AgentSession {
	e.T.Helper()
	s, err := store.AgentSessionByID(bg, e.H.App.DB.R, id)
	if err != nil {
		e.T.Fatal(err)
	}
	return s
}

func TestOpenSession(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	if !strings.HasPrefix(op.Token, "pxa_") || len(op.Token) != 4+43 {
		t.Fatalf("token %q", op.Token)
	}
	if !strings.Contains(op.Prompt, op.Token) {
		t.Fatal("the prompt does not hold the token")
	}
	sess := e.session(op.SessionID)
	if string(sess.TokenLookup) == op.Token || len(sess.TokenLookup) == 0 {
		t.Fatal("the token is stored as is")
	}
	if want := e.H.App.Vault.Lookup(op.Token); string(sess.TokenLookup) != string(want) {
		t.Fatal("token_lookup is not vault.Lookup(token)")
	}
	// Nothing in the database holds the token in the clear: not the session,
	// not an event.
	var n int
	if err := e.H.App.DB.R.Get(&n, `SELECT (SELECT count(*) FROM servers_agent_sessions WHERE problem LIKE '%pxa_%' OR context LIKE '%pxa_%')
		+ (SELECT count(*) FROM events WHERE payload LIKE '%pxa_%')`); err != nil || n != 0 {
		t.Fatalf("token leaked into the database (%d rows, %v)", n, err)
	}
	// The event notifies by default, and names the expiry.
	var typ *events.Type
	for i := range servers.Events {
		if servers.Events[i].Name == "template.agent_session_opened" {
			typ = &servers.Events[i]
		}
	}
	if typ == nil || !typ.Notify {
		t.Fatal("template.agent_session_opened does not notify")
	}
	evs := e.H.Events("template.agent_session_opened")
	if len(evs) != 1 || evs[0].Actor != "admin" || evs[0].Payload["expires_at"] == nil {
		t.Fatalf("events: %+v", evs)
	}
	if op.ExpiresAt != e.Now.Add(2*time.Hour) {
		t.Fatalf("default access is 2 h, expires %v", op.ExpiresAt)
	}
	// The token works.
	status(t, e.get(op.Token, "/draft"), http.StatusOK)
}

func TestOpenSessionNeedsAProblemAndADraft(t *testing.T) {
	e := newEnv(t)
	if _, err := e.Ag.OpenSession(bg, agent.Open{TemplateID: e.Tpl, Agent: "codex", Problem: "  "}, "admin"); err == nil {
		t.Fatal("an empty problem was accepted")
	}
	if _, err := e.Ag.OpenSession(bg, agent.Open{TemplateID: e.Tpl, Agent: "gpt", Problem: "x"}, "admin"); err == nil {
		t.Fatal("an unknown agent was accepted")
	}
	if err := e.H.Mod.Templates.DiscardDraft(bg, e.Tpl, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Ag.OpenSession(bg, agent.Open{TemplateID: e.Tpl, Agent: "codex", Problem: "x"}, "admin"); err == nil {
		t.Fatal("a template without a draft was handed over")
	}
}

func TestOneSessionPerDraft(t *testing.T) {
	e := newEnv(t)
	first := e.open(agent.Open{})
	second := e.open(agent.Open{Agent: "codex"})
	ended(t, e.get(first.Token, "/draft"))
	status(t, e.get(second.Token, "/draft"), http.StatusOK)
	if got := e.session(first.SessionID); got.CloseReason != "replaced" {
		t.Fatalf("first session closed as %q", got.CloseReason)
	}
	c := e.closed()
	if len(c) != 1 || c[0]["reason"] != "replaced" {
		t.Fatalf("closed events: %v", c)
	}
	cur, err := e.Ag.Current(bg, e.Tpl)
	if err != nil || cur.ID != second.SessionID {
		t.Fatalf("current session %v %v", cur.ID, err)
	}
}

func TestSessionExpires(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{For: 30 * time.Minute})
	status(t, e.get(op.Token, "/draft"), http.StatusOK)

	e.advance(31 * time.Minute)
	// Enforced on the request itself, before anything closes the session.
	ended(t, e.get(op.Token, "/draft"))
	if _, err := e.Ag.Current(bg, e.Tpl); err == nil {
		t.Fatal("an expired session is still current")
	}
	if len(e.closed()) != 0 {
		t.Fatal("closed before the schedule ran")
	}

	// The schedule is the job that records it.
	if s := e.Ag.Schedule(); s.Name != "servers.agent_sessions" || s.Every != 5*time.Minute {
		t.Fatalf("schedule %+v", s)
	}
	if _, err := e.H.App.Jobs.EnqueueNow(bg, jobs.Request{Type: agent.JobType, CreatedBy: "schedule:servers.agent_sessions"}); err != nil {
		t.Fatal(err)
	}
	e.H.Drain()
	c := e.closed()
	if len(c) != 1 || c[0]["reason"] != "expired" {
		t.Fatalf("closed events: %v", c)
	}
	// Once only.
	n, err := e.Ag.Expire(bg, "system")
	if err != nil || n != 0 || len(e.closed()) != 1 {
		t.Fatalf("expired again: %d %v", n, err)
	}
}

func TestExpiredSessionIsNotReplaced(t *testing.T) {
	e := newEnv(t)
	first := e.open(agent.Open{For: 30 * time.Minute})
	e.advance(time.Hour)
	e.open(agent.Open{})
	if got := e.session(first.SessionID); got.CloseReason != "expired" {
		t.Fatalf("an expired session was closed as %q", got.CloseReason)
	}
}

func TestRevoke(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	if err := e.Ag.Revoke(bg, op.SessionID, "admin"); err != nil {
		t.Fatal(err)
	}
	ended(t, e.get(op.Token, "/draft"))
	c := e.closed()
	if len(c) != 1 || c[0]["reason"] != "revoked" {
		t.Fatalf("closed events: %v", c)
	}
	if err := e.Ag.Revoke(bg, op.SessionID, "admin"); err != agent.ErrNotOpen {
		t.Fatalf("revoking twice: %v", err)
	}
	if len(e.closed()) != 1 {
		t.Fatal("a second revoke recorded another event")
	}
}

func TestDraftEndClosesSession(t *testing.T) {
	// Publish.
	e := newEnv(t)
	op := e.open(agent.Open{})
	d, err := e.H.Mod.Templates.Draft(bg, e.Tpl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.H.Mod.Templates.Publish(bg, e.Tpl, d.Revision, "by hand", true, "admin"); err != nil {
		t.Fatal(err)
	}
	ended(t, e.get(op.Token, "/draft"))
	if c := e.closed(); len(c) != 1 || c[0]["reason"] != "published" {
		t.Fatalf("closed events: %v", c)
	}

	// Discard.
	e = newEnv(t)
	op = e.open(agent.Open{})
	if err := e.H.Mod.Templates.DiscardDraft(bg, e.Tpl, "admin"); err != nil {
		t.Fatal(err)
	}
	ended(t, e.get(op.Token, "/draft"))
	if c := e.closed(); len(c) != 1 || c[0]["reason"] != "discarded" {
		t.Fatalf("closed events: %v", c)
	}
}

func TestEightHourCap(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{For: 72 * time.Hour})
	if want := e.Now.Add(8 * time.Hour); !op.ExpiresAt.Equal(want) {
		t.Fatalf("expires %v, want %v", op.ExpiresAt, want)
	}
	e.advance(8*time.Hour - time.Minute)
	status(t, e.get(op.Token, "/draft"), http.StatusOK)
	e.advance(2 * time.Minute)
	ended(t, e.get(op.Token, "/draft"))
}

// siteReq is a plain request to the whole site (admin pages included).
func siteReq(method, path string, h http.Header, cookies ...*http.Cookie) sitetest.Req {
	return sitetest.Req{Method: method, Path: path, Header: h, Cookies: cookies}
}

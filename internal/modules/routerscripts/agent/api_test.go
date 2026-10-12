package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/agent"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var bg = context.Background()

// script is today's annotated script with an @secret parameter added and a
// real-looking subscription link as subUrl's default.
func script() []byte {
	b := paramstest.Annotated()
	b = paramstest.Replace(b, `:local subUrl "https://files....t"`, `:local subUrl "https://proxier.example/s/link-token-1"`)
	b = paramstest.Replace(b, "# END PARAMETERS\n", "# router admin password\n# @secret\n:local adminPass \"pass word 1\"\n# END PARAMETERS\n")
	return b
}

var leaks = []string{"link-token-1", "pass word 1"}

type env struct {
	T  *testing.T
	H  *rscriptstest.Harness
	ID int64
}

// newEnv publishes script() as v1 and opens a draft of it.
func newEnv(t *testing.T) *env {
	t.Helper()
	h := rscriptstest.New(t)
	e := &env{T: t, H: h, ID: h.Script("fresh-router", script())}
	if _, err := h.Mod.Scripts.EditDraft(bg, e.ID, events.ActorAdmin); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) open(o agent.Open) agent.Opened {
	e.T.Helper()
	if o.ScriptID == 0 {
		o.ScriptID = e.ID
	}
	if o.Problem == "" {
		o.Problem = "the container does not start on 7.24.5; fix the script"
	}
	if o.Agent == "" {
		o.Agent = "claude-code"
	}
	op, err := e.H.Mod.Agent.OpenSession(bg, o, events.ActorAdmin)
	if err != nil {
		e.T.Fatalf("open session: %v", err)
	}
	return op
}

type req struct {
	Method, Path string
	Token        string
	Header       map[string]string
	Body         string
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
	sr := sitetest.Req{Method: r.Method, Path: agent.APIPrefix + r.Path, Header: h}
	if r.Body != "" {
		sr.Body, sr.ContentType = []byte(r.Body), "text/plain"
	}
	return e.H.Site.Do(sr)
}

func (e *env) get(token, path string) *httptest.ResponseRecorder {
	e.T.Helper()
	return e.do(req{Path: path, Token: token})
}

func (e *env) put(token, etag, body string) *httptest.ResponseRecorder {
	e.T.Helper()
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-Match"] = etag
	}
	return e.do(req{Method: http.MethodPut, Path: "/draft", Token: token, Header: hdr, Body: body})
}

func (e *env) draftBody() string {
	e.T.Helper()
	d, err := e.H.Mod.Scripts.Draft(bg, e.ID)
	if err != nil {
		e.T.Fatal(err)
	}
	return d.Body
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body.String())
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
}

func noLeak(t *testing.T, what, s string) {
	t.Helper()
	for _, l := range leaks {
		if strings.Contains(s, l) {
			t.Errorf("%s holds the secret %q", what, l)
		}
	}
}

type checkJSON struct {
	OK       bool `json:"ok"`
	Errors   int  `json:"errors"`
	Warnings int  `json:"warnings"`
	Findings []struct {
		Line     int    `json:"line"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"findings"`
	Ended      bool `json:"end_marker"`
	Parameters []struct {
		Name        string   `json:"name"`
		Default     string   `json:"default"`
		Hidden      bool     `json:"hidden"`
		Annotations []string `json:"annotations"`
	} `json:"parameters"`
	Computed []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"computed"`
}

func (c checkJSON) param(name string) (string, bool) {
	for _, p := range c.Parameters {
		if p.Name == name {
			return p.Default, p.Hidden
		}
	}
	return "", false
}

func TestOpenSessionRefusesAndRecords(t *testing.T) {
	e := newEnv(t)
	ag := e.H.Mod.Agent
	if _, err := ag.OpenSession(bg, agent.Open{ScriptID: e.ID, Problem: "  ", Agent: "codex"}, "admin"); !errors.Is(err, agent.ErrNoProblem) {
		t.Errorf("empty problem: %v", err)
	}
	if _, err := ag.OpenSession(bg, agent.Open{ScriptID: e.ID, Problem: strings.Repeat("я", 4001), Agent: "codex"}, "admin"); !errors.Is(err, agent.ErrNoProblem) {
		t.Errorf("long problem: %v", err)
	}
	if _, err := ag.OpenSession(bg, agent.Open{ScriptID: e.ID, Problem: "x", Agent: "gpt"}, "admin"); !errors.Is(err, agent.ErrBadAgent) {
		t.Errorf("unknown agent: %v", err)
	}
	if err := e.H.Mod.Scripts.DiscardDraft(bg, e.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.OpenSession(bg, agent.Open{ScriptID: e.ID, Problem: "x", Agent: "codex"}, "admin"); !errors.Is(err, agent.ErrNoDraft) {
		t.Errorf("no draft: %v", err)
	}
	if n := len(e.H.Events("routerscript.agent_session_opened")); n != 0 {
		t.Fatalf("refused hand-offs recorded %d events", n)
	}

	if _, err := e.H.Mod.Scripts.EditDraft(bg, e.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	op := e.open(agent.Open{For: 24 * time.Hour, IncludeDiff: true})
	if want := e.H.Now.Add(8 * time.Hour); !op.ExpiresAt.Equal(want) {
		t.Errorf("expires %v, want the 8-hour cap %v", op.ExpiresAt, want)
	}
	if !strings.HasPrefix(op.Token, "pxa_") || !strings.Contains(op.Prompt, op.Token) || !strings.Contains(op.Prompt, "http://") ||
		!strings.Contains(op.Prompt, agent.APIPrefix) || !strings.Contains(op.Prompt, "the container does not start") || !strings.Contains(op.Prompt, "based on version 1") {
		t.Errorf("prompt:\n%s", op.Prompt)
	}
	ev := e.H.Events("routerscript.agent_session_opened")
	if len(ev) != 1 || ev[0].Actor != "admin" || ev[0].Payload["agent"] != "claude-code" {
		t.Fatalf("opened: %+v", ev)
	}
	raw, _ := json.Marshal(ev[0].Payload)
	if strings.Contains(string(raw), op.Token) || strings.Contains(string(raw), "pxa_") {
		t.Error("the opened event holds the token")
	}
}

func TestEndedExpiredAndUnknownTokensLookTheSame(t *testing.T) {
	e := newEnv(t)
	first := e.open(agent.Open{For: 30 * time.Minute})
	status(t, e.get(first.Token, "/draft"), http.StatusOK)
	second := e.open(agent.Open{})
	ended := e.get(first.Token, "/draft")
	status(t, ended, http.StatusUnauthorized)
	closed := e.H.Events("routerscript.agent_session_closed")
	if len(closed) != 1 || closed[0].Payload["reason"] != "replaced" {
		t.Fatalf("closed: %+v", closed)
	}
	unknown := e.get("pxa_"+strings.Repeat("A", 43), "/draft")
	none := e.get("", "/draft")
	cookie := e.H.Site.Do(sitetest.Req{Path: agent.APIPrefix + "/draft", Cookies: []*http.Cookie{e.H.Login.Cookie}})
	for name, rec := range map[string]*httptest.ResponseRecorder{"unknown": unknown, "no token": none, "a session cookie": cookie} {
		if rec.Code != ended.Code || rec.Body.String() != ended.Body.String() || rec.Header().Get("WWW-Authenticate") != ended.Header().Get("WWW-Authenticate") {
			t.Errorf("%s: %d %s, an ended token: %d %s", name, rec.Code, rec.Body.String(), ended.Code, ended.Body.String())
		}
	}
	if !strings.Contains(ended.Body.String(), "session ended") {
		t.Errorf("ended: %s", ended.Body.String())
	}

	status(t, e.get(second.Token, "/draft"), http.StatusOK)
	e.H.Advance(2*time.Hour + time.Second)
	status(t, e.get(second.Token, "/draft"), http.StatusUnauthorized)
	// the schedule records the expiry; a hand-off after it says expired, not replaced
	n, err := e.H.Mod.Agent.Expire(bg, "job:7")
	if err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	closed = e.H.Events("routerscript.agent_session_closed")
	if len(closed) != 2 || closed[1].Payload["reason"] != "expired" || closed[1].Actor != "job:7" {
		t.Fatalf("closed: %+v", closed)
	}
	if n, _ := e.H.Mod.Agent.Expire(bg, "job:8"); n != 0 {
		t.Errorf("a second scan closed %d", n)
	}
	third := e.open(agent.Open{For: 30 * time.Minute})
	e.H.Advance(time.Hour)
	e.open(agent.Open{})
	closed = e.H.Events("routerscript.agent_session_closed")
	if last := closed[len(closed)-1]; last.Payload["reason"] != "expired" || last.Payload["session"] != float64(third.SessionID) {
		t.Fatalf("a hand-off after an unrecorded expiry: %+v", last)
	}
}

func TestAgentReadsSecretsMasked(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	rec := e.get(op.Token, "/draft")
	status(t, rec, http.StatusOK)
	body := rec.Body.String()
	noLeak(t, "GET /draft", body)
	if !strings.Contains(body, `:local subUrl "MASKED-BY-PROXIER"`) || !strings.Contains(body, `:local adminPass "MASKED-BY-PROXIER"`) || !strings.Contains(body, `:local lanNet "10.230.1"`) {
		t.Fatalf("draft as the agent reads it:\n%s", body)
	}
	d, _ := e.H.Mod.Scripts.Draft(bg, e.ID)
	if rec.Header().Get("ETag") != `"`+strconv.Itoa(d.Revision)+`"` || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("headers: %v", rec.Header())
	}
	ctx := e.get(op.Token, "/context")
	status(t, ctx, http.StatusOK)
	noLeak(t, "GET /context", ctx.Body.String())
	chk := e.do(req{Method: http.MethodPost, Path: "/draft/check", Token: op.Token})
	status(t, chk, http.StatusOK)
	noLeak(t, "POST /draft/check", chk.Body.String())
	var out struct {
		Saved bool      `json:"saved"`
		Check checkJSON `json:"check"`
	}
	decode(t, chk, &out)
	if v, hidden := out.Check.param("subUrl"); !out.Saved || v != agent.Masked || !hidden {
		t.Errorf("subUrl in the check: %q %v (saved %v)", v, hidden, out.Saved)
	}
	if v, hidden := out.Check.param("lanNet"); v != "10.230.1" || hidden {
		t.Errorf("lanNet in the check: %q %v", v, hidden)
	}
}

func TestAgentSaveNeedsTheETagAndKeepsSecrets(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	rec := e.get(op.Token, "/draft")
	etag := rec.Header().Get("ETag")
	edited := strings.Replace(rec.Body.String(), `:local lanNet "10.230.1"`, `:local lanNet "10.240.1"`, 1)
	before := len(e.H.Events("routerscript.draft_saved"))

	status(t, e.put(op.Token, "", edited), http.StatusPreconditionRequired)
	status(t, e.put(op.Token, `"99"`, edited), http.StatusPreconditionFailed)
	if len(e.H.Events("routerscript.draft_saved")) != before || strings.Contains(e.draftBody(), "10.240.1") {
		t.Fatal("a refused write changed the draft")
	}
	ok := e.put(op.Token, etag, edited)
	status(t, ok, http.StatusOK)
	var out struct {
		Revision int       `json:"revision"`
		ETag     string    `json:"etag"`
		Changed  bool      `json:"changed"`
		Check    checkJSON `json:"check"`
	}
	decode(t, ok, &out)
	if !out.Changed || out.ETag == etag || ok.Header().Get("ETag") != out.ETag || !out.Check.OK || !out.Check.Ended {
		t.Fatalf("answer: %s", ok.Body.String())
	}
	want := strings.Replace(string(script()), "10.230.1", "10.240.1", 1)
	if got := e.draftBody(); got != want {
		t.Fatalf("saved draft lost a secret or changed more:\n%s", got)
	}
	ev := e.H.Events("routerscript.draft_saved")
	if len(ev) != before+1 || ev[len(ev)-1].Actor != "agent:"+strconv.FormatInt(op.SessionID, 10) {
		t.Fatalf("draft_saved: %+v", ev)
	}
	sess, _ := e.H.Mod.Agent.Current(bg, e.ID)
	if sess.Saves != 1 {
		t.Errorf("saves %d", sess.Saves)
	}
	// the old ETag is stale now; the same text again changes nothing
	status(t, e.put(op.Token, etag, edited), http.StatusPreconditionFailed)
	same := e.put(op.Token, out.ETag, edited)
	status(t, same, http.StatusOK)
	if strings.Contains(same.Body.String(), `"changed":true`) || len(e.H.Events("routerscript.draft_saved")) != before+1 {
		t.Errorf("an unchanged save: %s", same.Body.String())
	}
	if sess, _ := e.H.Mod.Agent.Current(bg, e.ID); sess.Saves != 1 {
		t.Errorf("an unchanged save counted: %d", sess.Saves)
	}
}

func TestAgentSaveRefusals(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	rec := e.get(op.Token, "/draft")
	etag, masked := rec.Header().Get("ETag"), rec.Body.String()
	stored := e.draftBody()

	renamed := strings.Replace(masked, ":local adminPass ", ":local rootPass ", 1)
	left := e.put(op.Token, etag, renamed)
	status(t, left, http.StatusUnprocessableEntity)
	if !strings.Contains(left.Body.String(), "adminPass") && !strings.Contains(left.Body.String(), "rootPass") {
		t.Errorf("mask refusal names no parameter: %s", left.Body.String())
	}
	status(t, e.put(op.Token, etag, "\x00binary"), http.StatusUnprocessableEntity)
	big := e.put(op.Token, etag, strings.Repeat("# x\n", 600<<10/4))
	status(t, big, http.StatusUnprocessableEntity)
	if !strings.Contains(big.Body.String(), "512 KiB") {
		t.Errorf("large: %s", big.Body.String())
	}
	status(t, e.put(op.Token, etag, strings.Repeat("x", 2<<20)), http.StatusRequestEntityTooLarge)
	if e.draftBody() != stored {
		t.Fatal("a refused save changed the draft")
	}
	if sess, _ := e.H.Mod.Agent.Current(bg, e.ID); sess.Saves != 0 {
		t.Errorf("refused saves counted: %d", sess.Saves)
	}
}

func TestAgentSaveKeepsCRLF(t *testing.T) {
	e := newEnv(t)
	d, _ := e.H.Mod.Scripts.Draft(bg, e.ID)
	crlf := strings.ReplaceAll(d.Body, "\n", "\r\n")
	if _, err := e.H.Mod.Scripts.SaveDraft(bg, e.ID, d.Revision, crlf, "admin"); err != nil {
		t.Fatal(err)
	}
	op := e.open(agent.Open{})
	rec := e.get(op.Token, "/draft")
	lf := strings.ReplaceAll(rec.Body.String(), "\r\n", "\n")
	status(t, e.put(op.Token, rec.Header().Get("ETag"), strings.Replace(lf, "10.230.1", "10.240.1", 1)), http.StatusOK)
	if got, want := e.draftBody(), strings.Replace(crlf, "10.230.1", "10.240.1", 1); got != want {
		t.Fatal("an LF save of a CRLF draft changed its line endings or lost a secret")
	}
}

func TestAgentAndAdminEditTogether(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	rec := e.get(op.Token, "/draft")
	d, _ := e.H.Mod.Scripts.Draft(bg, e.ID)
	if _, err := e.H.Mod.Scripts.SaveDraft(bg, e.ID, d.Revision, d.Body+"\n:put \"admin\"\n", "admin"); err != nil {
		t.Fatal(err)
	}
	status(t, e.put(op.Token, rec.Header().Get("ETag"), rec.Body.String()+"# agent\n"), http.StatusPreconditionFailed)
	if strings.Contains(e.draftBody(), "# agent") {
		t.Fatal("a stale agent write overwrote the admin's save")
	}
}

func TestCheckAnUnsavedBody(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	masked := e.get(op.Token, "/draft").Body.String()
	stored := e.draftBody()
	broken := strings.Replace(masked, "# END PARAMETERS\n", ":local lanNet \"10.1.1\"\n# END PARAMETERS\n", 1)
	rec := e.do(req{Method: http.MethodPost, Path: "/draft/check", Token: op.Token, Body: broken})
	status(t, rec, http.StatusOK)
	var out struct {
		Saved bool      `json:"saved"`
		Check checkJSON `json:"check"`
	}
	decode(t, rec, &out)
	if out.Saved || out.Check.OK || out.Check.Errors != 1 || out.Check.Findings[0].Message != "Duplicate parameter lanNet." {
		t.Fatalf("check of a duplicate: %s", rec.Body.String())
	}
	if e.draftBody() != stored || len(e.H.Events("routerscript.draft_saved")) != 1 {
		t.Fatal("a check saved")
	}
	// a computed value made from a hidden default stays hidden
	derived := strings.Replace(masked, "# END PARAMETERS\n", ":local passCopy ($adminPass . \"!\")\n# END PARAMETERS\n", 1)
	rec = e.do(req{Method: http.MethodPost, Path: "/draft/check", Token: op.Token, Body: derived})
	status(t, rec, http.StatusOK)
	noLeak(t, "a computed value", rec.Body.String())
	decode(t, rec, &out)
	for _, c := range out.Check.Computed {
		if c.Name == "passCopy" && c.Value != agent.Masked+"!" {
			t.Errorf("passCopy = %q", c.Value)
		}
	}
	// the gone-parameter warning compares with the base version
	gone := strings.Replace(masked, ":local proxierKey \"\"\n", "", 1)
	rec = e.do(req{Method: http.MethodPost, Path: "/draft/check", Token: op.Token, Body: gone})
	decode(t, rec, &out)
	if out.Check.Warnings != 1 || !strings.Contains(out.Check.Findings[0].Message, "proxierKey") {
		t.Errorf("gone parameter: %s", rec.Body.String())
	}
}

func TestContextAndDiff(t *testing.T) {
	e := newEnv(t)
	d, _ := e.H.Mod.Scripts.Draft(bg, e.ID)
	// the draft changes a secret and a plain value; the diff shows only the plain one
	body := strings.Replace(strings.Replace(d.Body, "pass word 1", "pass word 2", 1), "10.230.1", "10.240.1", 1)
	if _, err := e.H.Mod.Scripts.SaveDraft(bg, e.ID, d.Revision, body, "admin"); err != nil {
		t.Fatal(err)
	}
	op := e.open(agent.Open{IncludeDiff: true})
	rec := e.get(op.Token, "/context")
	status(t, rec, http.StatusOK)
	noLeak(t, "the context", rec.Body.String())
	if strings.Contains(rec.Body.String(), "pass word 2") {
		t.Fatal("the diff holds the new secret")
	}
	var out struct {
		Problem string  `json:"problem"`
		Diff    *string `json:"diff"`
		Masked  string  `json:"masked_value"`
		Script  struct {
			Name        string `json:"name"`
			BaseVersion int    `json:"base_version"`
			Revision    int    `json:"revision"`
		} `json:"script"`
		Check checkJSON `json:"check"`
	}
	decode(t, rec, &out)
	if out.Diff == nil || !strings.Contains(*out.Diff, "+:local lanNet \"10.240.1\"") || strings.Contains(*out.Diff, "adminPass") ||
		out.Script.Name != "fresh-router" || out.Script.BaseVersion != 1 || out.Script.Revision != 2 || out.Masked != agent.Masked || !out.Check.OK ||
		!strings.Contains(out.Problem, "container") {
		t.Fatalf("context: %s", rec.Body.String())
	}
	without := e.open(agent.Open{})
	rec = e.get(without.Token, "/context")
	decode(t, rec, &out)
	if out.Diff != nil || !strings.Contains(rec.Body.String(), `"diff":null`) {
		t.Errorf("diff not asked for: %s", rec.Body.String())
	}
}

func TestPublishDiscardRevokeAndDeleteEndTheSession(t *testing.T) {
	e := newEnv(t)
	ag, sc := e.H.Mod.Agent, e.H.Mod.Scripts
	closedWith := func(reason string) {
		t.Helper()
		ev := e.H.Events("routerscript.agent_session_closed")
		if len(ev) == 0 || ev[len(ev)-1].Payload["reason"] != reason {
			t.Fatalf("want closed %s: %+v", reason, ev)
		}
	}

	op := e.open(agent.Open{})
	d, _ := sc.Draft(bg, e.ID)
	if _, err := sc.Publish(bg, e.ID, d.Revision, "", true, "admin"); err != nil {
		t.Fatal(err)
	}
	closedWith("published")
	status(t, e.get(op.Token, "/draft"), http.StatusUnauthorized)

	if _, err := sc.EditDraft(bg, e.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	op = e.open(agent.Open{})
	if err := sc.DiscardDraft(bg, e.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	closedWith("discarded")
	status(t, e.get(op.Token, "/draft"), http.StatusUnauthorized)

	if _, err := sc.EditDraft(bg, e.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	op = e.open(agent.Open{})
	other := e.H.Script("other", paramstest.WithEnd(paramstest.Today()))
	if err := ag.Revoke(bg, other, op.SessionID, "admin"); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("revoking through another script: %v", err)
	}
	if err := ag.Revoke(bg, e.ID, op.SessionID, "admin"); err != nil {
		t.Fatal(err)
	}
	closedWith("revoked")
	if err := ag.Revoke(bg, e.ID, op.SessionID, "admin"); !errors.Is(err, agent.ErrNotOpen) {
		t.Errorf("a second revoke: %v", err)
	}
	status(t, e.get(op.Token, "/draft"), http.StatusUnauthorized)

	if _, err := sc.EditDraft(bg, other, "admin"); err != nil {
		t.Fatal(err)
	}
	op = e.open(agent.Open{ScriptID: other})
	if err := sc.Delete(bg, other, "admin"); err != nil {
		t.Fatal(err)
	}
	status(t, e.get(op.Token, "/draft"), http.StatusUnauthorized)
}

func TestAgentRateLimits(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	for i := 0; i < 60; i++ {
		status(t, e.get(op.Token, "/draft"), http.StatusOK)
	}
	rec := e.get(op.Token, "/draft")
	status(t, rec, http.StatusTooManyRequests)
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > 61 {
		t.Errorf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	e.H.Advance(time.Minute)
	status(t, e.get(op.Token, "/draft"), http.StatusOK)
	e.H.Exec(`UPDATE rscripts_agent_sessions SET requests = 600 WHERE id = ?`, op.SessionID)
	status(t, e.get(op.Token, "/draft"), http.StatusTooManyRequests)
}

func TestParameterDocsInSync(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/modules/router-scripts.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	from, to := strings.Index(doc, "## Parameters\n"), strings.Index(doc, "## Generations\n")
	if from < 0 || to < from {
		t.Fatal("the Parameters section of docs/modules/router-scripts.md moved: update this test")
	}
	want := strings.TrimRight(doc[from:to], "\n") + "\n"
	if agent.ParameterDocs != want {
		t.Fatal("routerscripts/agent/parameters.md differs from the Parameters section of docs/modules/router-scripts.md: copy the section again")
	}
	e := newEnv(t)
	op := e.open(agent.Open{})
	rec := e.get(op.Token, "/docs/parameters")
	status(t, rec, http.StatusOK)
	if rec.Body.String() != want || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("served docs differ (%s)", rec.Header().Get("Content-Type"))
	}
}

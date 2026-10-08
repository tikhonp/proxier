package pages_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var (
	formIDRe  = regexp.MustCompile(`name="form_id" value="([0-9a-f]+)"`)
	dataCopy  = regexp.MustCompile(`data-copy="([^"]*)"`)
	revisionR = regexp.MustCompile(`id="ed-revision" value="(\d+)"`)
	tokenRe   = regexp.MustCompile(`pxa_[A-Za-z0-9_-]{43}`)
)

// handOffEnv is a harness with the seed template's draft open.
func handOffEnv(t *testing.T) (*serverstest.Harness, string) {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	tid := strconv.FormatInt(h.TemplateID, 10)
	page(t, h, "/templates/"+tid+"/edit") // starts the draft
	return h, tid
}

// handOffForm reads a fresh form and fills it in.
func handOffForm(h *serverstest.Harness, tid string) url.Values {
	h.T.Helper()
	body := page(h.T, h, "/templates/"+tid+"/agent")
	m := formIDRe.FindStringSubmatch(body)
	if m == nil {
		h.T.Fatalf("the form has no form_id:\n%s", excerpt(body))
	}
	return url.Values{"form_id": {m[1]}, "problem": {"nginx will not start"}, "agent": {"claude-code"}, "for": {"120"}, "report": {"1"}, "diff": {"1"}}
}

// handOff posts the form and returns the token the page hands over, taken from
// the copy button.
func handOff(h *serverstest.Harness, tid string) (token string, body string) {
	h.T.Helper()
	rec := h.Login.Post("/templates/"+tid+"/agent", handOffForm(h, tid))
	if rec.Code != 200 {
		h.T.Fatalf("hand-off: %d\n%s", rec.Code, excerpt(rec.Body.String()))
	}
	body = rec.Body.String()
	m := dataCopy.FindStringSubmatch(body)
	if m == nil {
		h.T.Fatalf("no copy button:\n%s", excerpt(body))
	}
	token = tokenRe.FindString(html.UnescapeString(m[1]))
	if token == "" {
		h.T.Fatal("the copied prompt holds no token")
	}
	return token, body
}

func apiCall(h *serverstest.Harness, token, method, path string, hdr map[string]string, body []byte) *httptest.ResponseRecorder {
	h.T.Helper()
	hh := http.Header{}
	hh.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		hh.Set(k, v)
	}
	r := sitetest.Req{Method: method, Path: "/agent/v1" + path, Header: hh}
	if body != nil {
		r.Body, r.ContentType = body, "application/octet-stream"
	}
	return h.Site.Do(r)
}

func TestHandOffDialog(t *testing.T) {
	h, tid := handOffEnv(t)

	// The form.
	body := page(t, h, "/templates/"+tid+"/agent")
	mustContain(t, body, "Hand off the draft to an agent", `name="problem"`, `name="agent"`, `name="for"`, "Validation report", "Diff against the base version", "It can", "It can&#39;t", "Claude Code", "Codex")
	mustNotContain(t, body, "pxa_")
	if h.Login.Get("/templates/"+tid).Code != 200 || !strings.Contains(page(t, h, "/templates/"+tid), "/templates/"+tid+"/agent") {
		t.Fatal("the template page does not link to the hand-off")
	}

	// An empty problem opens nothing.
	f := handOffForm(h, tid)
	f.Set("problem", "   ")
	if rec := h.Login.Post("/templates/"+tid+"/agent", f); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Say what the agent should do") {
		t.Fatalf("empty problem: %d\n%s", rec.Code, excerpt(rec.Body.String()))
	}
	if n := len(h.Events("template.agent_session_opened")); n != 0 {
		t.Fatalf("%d sessions opened for a refused form", n)
	}

	// Opening: the page shows the prompt with the token masked; the copy button
	// holds the real one.
	f = handOffForm(h, tid)
	rec := h.Login.Post("/templates/"+tid+"/agent", f)
	if rec.Code != 200 {
		t.Fatalf("open: %d\n%s", rec.Code, excerpt(rec.Body.String()))
	}
	opened := rec.Body.String()
	m := dataCopy.FindStringSubmatch(opened)
	if m == nil {
		t.Fatalf("no Copy prompt button:\n%s", excerpt(opened))
	}
	prompt := html.UnescapeString(m[1])
	token := tokenRe.FindString(prompt)
	if token == "" || !strings.Contains(prompt, "nginx will not start") || !strings.Contains(prompt, "/agent/v1") || !strings.Contains(prompt, "Never publish") {
		t.Fatalf("the copied prompt:\n%s", prompt)
	}
	visible := dataCopy.ReplaceAllString(opened, "")
	mustContain(t, visible, "Copy prompt", "pxa_••••", "nginx will not start", "the token is shown once", "Anyone who has it can edit this draft")
	if strings.Contains(visible, token) || tokenRe.MatchString(visible) {
		t.Fatal("the page shows the token")
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control %q", got)
	}
	// The token works.
	if rec := apiCall(h, token, "GET", "/draft", nil, nil); rec.Code != 200 {
		t.Fatalf("the token: %d", rec.Code)
	}

	// A reload posts the form again: the prompt is not shown, no second session.
	again := h.Login.Post("/templates/"+tid+"/agent", f)
	if again.Code != 200 {
		t.Fatalf("repost: %d", again.Code)
	}
	mustContain(t, again.Body.String(), "Already copied", "Open a new hand-off")
	mustNotContain(t, again.Body.String(), "pxa_", "data-copy=")
	if n := len(h.Events("template.agent_session_opened")); n != 1 {
		t.Fatalf("%d sessions opened", n)
	}
	if rec := apiCall(h, token, "GET", "/draft", nil, nil); rec.Code != 200 {
		t.Fatalf("the first token stopped working: %d", rec.Code)
	}
	// And the form itself never shows a token.
	mustNotContain(t, page(t, h, "/templates/"+tid+"/agent"), "pxa_")

	// A new hand-off replaces the first.
	second, _ := handOff(h, tid)
	if rec := apiCall(h, token, "GET", "/draft", nil, nil); rec.Code != 401 {
		t.Fatalf("the replaced token: %d", rec.Code)
	}
	if rec := apiCall(h, second, "GET", "/draft", nil, nil); rec.Code != 200 {
		t.Fatalf("the new token: %d", rec.Code)
	}
}

func TestSessionBand(t *testing.T) {
	h, tid := handOffEnv(t)
	mustNotContain(t, page(t, h, "/templates/"+tid), "Revoke access")

	token, _ := handOff(h, tid)
	var sid int64
	if err := h.App.DB.R.Get(&sid, `SELECT id FROM servers_agent_sessions`); err != nil {
		t.Fatal(err)
	}
	actor := "agent:" + strconv.FormatInt(sid, 10)

	// The agent saves a file and validates.
	status := apiCall(h, token, "PUT", "/draft/files/notes.txt", map[string]string{"If-None-Match": "*"}, []byte("hi")).Code
	if status != 201 {
		t.Fatalf("agent save: %d", status)
	}
	if code := apiCall(h, token, "POST", "/draft/validate", nil, nil).Code; code != 200 {
		t.Fatalf("agent validate: %d", code)
	}

	for _, p := range []string{"/templates/" + tid, "/templates/" + tid + "/edit"} {
		body := page(t, h, p)
		mustContain(t, body, "Handed to Claude Code", "nginx will not start", "1 saves · 1 validations", "expires", "What it changed", "/activity?actor="+actor, "Revoke access", "/templates/"+tid+"/agent/"+strconv.FormatInt(sid, 10)+"/revoke")
	}

	// What it changed: Activity filtered by the agent.
	act := page(t, h, "/activity?actor="+url.QueryEscape(actor))
	mustContain(t, act, "agent #"+strconv.FormatInt(sid, 10))
	mustNotContain(t, act, "Handed the draft") // the admin's own event is not the agent's

	// Revoke: the token stops, the band goes, the draft keeps the agent's file.
	rec := h.Login.Post("/templates/"+tid+"/agent/"+strconv.FormatInt(sid, 10)+"/revoke", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke: %d\n%s", rec.Code, excerpt(rec.Body.String()))
	}
	if code := apiCall(h, token, "GET", "/draft", nil, nil).Code; code != 401 {
		t.Fatalf("revoked token: %d", code)
	}
	after := page(t, h, "/templates/"+tid+"?saved=revoked")
	mustContain(t, after, "The agent&#39;s access was revoked.")
	mustNotContain(t, after, "Revoke access", "Handed to")
	if d, err := h.Mod.Templates.Draft(bg, h.TemplateID); err != nil || string(d.Files["notes.txt"]) != "hi" {
		t.Fatalf("the agent's file is gone from the draft: %v", err)
	}
	// Revoking a session of another template is a 404.
	if rec := h.Login.Post("/templates/9999/agent/"+strconv.FormatInt(sid, 10)+"/revoke", nil); rec.Code != 404 {
		t.Fatalf("revoke through another template: %d", rec.Code)
	}
}

func TestEditorSeesAgentSave(t *testing.T) {
	h, tid := handOffEnv(t)
	poll := "/templates/" + tid + "/draft/revision"

	// Without a session the editor does not poll.
	body := page(t, h, "/templates/"+tid+"/edit")
	mustNotContain(t, body, `id="ed-poll"`)

	token, _ := handOff(h, tid)
	body = page(t, h, "/templates/"+tid+"/edit")
	mustContain(t, body, `id="ed-poll"`, `hx-get="`+poll+`"`, `hx-trigger="every 10s"`, `hx-include="#ed-revision"`)
	rev := revisionR.FindStringSubmatch(body)[1]

	// Nothing changed: the same poller comes back, no band.
	got := page(t, h, poll+"?revision="+rev)
	mustContain(t, got, `id="ed-poll"`, "every 10s")
	mustNotContain(t, got, "changed since")

	// The agent saves: the poll answers with the band.
	if code := apiCall(h, token, "PUT", "/draft/files/notes.txt", map[string]string{"If-None-Match": "*"}, []byte("hi")).Code; code != 201 {
		t.Fatalf("agent save: %d", code)
	}
	got = page(t, h, poll+"?revision="+rev)
	mustContain(t, got, "The draft changed since you opened it", "data-ed-stale", "data-copy-draft", "data-ed-reload")
	mustNotContain(t, got, "every 10s") // it stops asking

	// The editor's own save with the old revision is refused and loses nothing.
	form := url.Values{"revision": {rev}, "active": {"manifest.yaml"}, "file[manifest.yaml]": {"name: mine\n"}}
	rec := h.Login.Post("/templates/"+tid+"/draft", form)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a stale save: %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "The draft changed since you opened it", "name: mine")

	// When the session has ended the poll stops once the editor is current.
	cur := revisionR.FindStringSubmatch(page(t, h, "/templates/"+tid+"/edit"))[1]
	var sid int64
	if err := h.App.DB.R.Get(&sid, `SELECT id FROM servers_agent_sessions`); err != nil {
		t.Fatal(err)
	}
	if rec := h.Login.Post("/templates/"+tid+"/agent/"+strconv.FormatInt(sid, 10)+"/revoke", nil); rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	if body := page(t, h, poll+"?revision="+cur); strings.TrimSpace(body) != "" {
		t.Fatalf("the poll goes on after the session ended: %q", body)
	}
}

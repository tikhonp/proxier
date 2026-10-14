package pages_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/events"
)

func handoffScript(t *testing.T) (*rscriptstest.Harness, int64, string) {
	t.Helper()
	h := rscriptstest.New(t)
	body := paramstest.Annotate(paramstest.Annotated(), "lanNet", "@secret")
	id := h.Script("fresh-router", body)
	if _, err := h.Mod.Scripts.EditDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	return h, id, "/router-scripts/" + strconv.FormatInt(id, 10)
}

func openSession(t *testing.T, h *rscriptstest.Harness, id int64) store.AgentSession {
	t.Helper()
	sess, err := h.Mod.Agent.Current(ctx, id)
	if err != nil {
		t.Fatalf("no open session: %v", err)
	}
	return sess
}

func TestHandOffFormOpensASessionOnce(t *testing.T) {
	h, id, base := handoffScript(t)
	page := h.Login.Get(base).Body.String()
	if !strings.Contains(page, `href="`+base+`/agent"`) || !strings.Contains(page, "Hand off to an agent…") || strings.Contains(page, `id="ed-poll"`) {
		t.Fatal("the draft has no hand-off button, or polls with no session")
	}
	form := h.Login.Get(base + "/agent")
	if form.Code != http.StatusOK {
		t.Fatalf("form: %d", form.Code)
	}
	fb := form.Body.String()
	checkNoInline(t, "hand-off form", fb)
	for _, want := range []string{"draft based on v1", "Diff against the base version", "lanNet, subUrl", "MASKED-BY-PROXIER", `name="form_id"`} {
		if !strings.Contains(html.UnescapeString(fb), want) {
			t.Errorf("form lacks %q", want)
		}
	}
	fid := between(fb, `name="form_id" value="`, `"`)

	bad := h.Login.Post(base+"/agent", url.Values{"problem": {"  "}, "agent": {"codex"}, "for": {"30"}, "form_id": {fid + "x"}})
	if bad.Code != http.StatusUnprocessableEntity || !strings.Contains(bad.Body.String(), "Say what the agent should do") {
		t.Fatalf("empty problem: %d", bad.Code)
	}
	post := url.Values{"problem": {"the container does not start"}, "agent": {"codex"}, "for": {"30"}, "diff": {"1"}, "form_id": {fid}}
	rec := h.Login.Post(base+"/agent", post)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("open: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	checkNoInline(t, "hand-off result", rec.Body.String())
	res := html.UnescapeString(rec.Body.String())
	tok := between(html.UnescapeString(between(rec.Body.String(), `data-copy="`, `"`)), "Bearer ", "`")
	if !strings.HasPrefix(tok, "pxa_") || len(tok) < 40 {
		t.Fatalf("no token in the copy button: %q", tok)
	}
	shown := between(res, `<pre class="agent-prompt" tabindex="0">`, "</pre>")
	if strings.Contains(shown, tok) || !strings.Contains(shown, "pxa_••••") || !strings.Contains(shown, "You are Codex") {
		t.Fatalf("shown prompt:\n%s", shown)
	}
	if warn := between(res, `<div class="alert gold" role="status">`, "</div>"); !strings.Contains(warn, "until the access expires <time datetime=") || !strings.Contains(warn, "</time>. To end it sooner, revoke access on the script page.") {
		t.Errorf("warning: %s", warn)
	}
	sess := openSession(t, h, id)
	if sess.Agent != "codex" || sess.ExpiresAt.Sub(sess.OpenedAt.Time).Minutes() != 30 || !strings.Contains(sess.Context, `"diff":true`) {
		t.Errorf("session: %+v", sess)
	}
	// posting the same form again (a reload) hands nothing over
	again := h.Login.Post(base+"/agent", post)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), "Already copied") || strings.Contains(again.Body.String(), "pxa_") {
		t.Fatalf("repost: %d", again.Code)
	}
	if n := len(h.Events("routerscript.agent_session_opened")); n != 1 {
		t.Fatalf("%d sessions opened", n)
	}
	if cur := openSession(t, h, id); cur.ID != sess.ID {
		t.Fatal("the repost replaced the session")
	}
}

func TestHandOffWithoutADraft(t *testing.T) {
	h, id, base := handoffScript(t)
	if err := h.Mod.Scripts.DiscardDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.Login.Get(base).Body.String(), base+"/agent") {
		t.Error("a script without a draft offers the hand-off")
	}
	for _, rec := range []*httptest.ResponseRecorder{
		h.Login.Get(base + "/agent"),
		h.Login.Post(base+"/agent", url.Values{"problem": {"x"}, "agent": {"codex"}}),
	} {
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != base {
			t.Errorf("no draft: %d %q", rec.Code, rec.Header().Get("Location"))
		}
	}
	if n := len(h.Events("routerscript.agent_session_opened")); n != 0 {
		t.Fatalf("%d sessions opened", n)
	}
}

func TestBandRevokeAndPoll(t *testing.T) {
	h, id, base := handoffScript(t)
	rec := h.Login.Post(base+"/agent", url.Values{"problem": {"fix the DoH forwarder\nsecond line"}, "agent": {"claude-code"}, "for": {"120"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("open: %d", rec.Code)
	}
	sess := openSession(t, h, id)
	page := h.Login.Get(base).Body.String()
	checkNoInline(t, "script page with a session", page)
	revoke := base + "/agent/" + strconv.FormatInt(sess.ID, 10) + "/revoke"
	for _, want := range []string{"Handed to Claude Code", "fix the DoH forwarder", "0 saves · expires ", "/activity?module=routerscripts&actor=agent:" + strconv.FormatInt(sess.ID, 10), revoke, `id="ed-poll"`, `hx-include="#ed-revision"`, `id="ed-revision"`} {
		if !strings.Contains(html.UnescapeString(page), want) {
			t.Errorf("script page lacks %q", want)
		}
	}
	if strings.Contains(page, "second line") {
		t.Error("the band shows more than the problem's first line")
	}

	d, _ := h.Mod.Scripts.Draft(ctx, id)
	poll := func(rev int) string {
		t.Helper()
		r := h.Login.Get(base + "/draft/revision?revision=" + strconv.Itoa(rev))
		if r.Code != http.StatusOK {
			t.Fatalf("poll: %d", r.Code)
		}
		return r.Body.String()
	}
	if p := poll(d.Revision); !strings.Contains(p, `hx-get="`+base+`/draft/revision"`) || strings.Contains(p, "data-ed-stale") {
		t.Fatalf("poll, same revision: %s", p)
	}
	if _, err := h.Mod.Scripts.SaveDraft(ctx, id, d.Revision, d.Body+"\n# by the agent\n", events.AgentActor(sess.ID)); err != nil {
		t.Fatal(err)
	}
	if p := poll(d.Revision); !strings.Contains(p, "data-ed-stale") || !strings.Contains(p, "an agent or another tab saved it") || strings.Contains(p, "hx-get") {
		t.Fatalf("poll after a save: %s", p)
	}

	other := h.Script("other", paramstest.WithEnd(paramstest.Today()))
	wrong := h.Login.Post("/router-scripts/"+strconv.FormatInt(other, 10)+"/agent/"+strconv.FormatInt(sess.ID, 10)+"/revoke", nil)
	if wrong.Code != http.StatusNotFound {
		t.Errorf("revoke through another script: %d", wrong.Code)
	}
	r := h.Login.Post(revoke, nil)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != base+"?revoked=1#draft" {
		t.Fatalf("revoke: %d %q", r.Code, r.Header().Get("Location"))
	}
	if r := h.Login.Post(revoke, nil); r.Code != http.StatusSeeOther {
		t.Errorf("a second revoke: %d", r.Code)
	}
	page = h.Login.Get(base + "?revoked=1").Body.String()
	if !strings.Contains(page, "The agent&#39;s access was revoked.") || strings.Contains(page, "Handed to") || strings.Contains(page, `id="ed-poll"`) {
		t.Error("the band stayed after revoking")
	}
	d, _ = h.Mod.Scripts.Draft(ctx, id)
	if p := poll(d.Revision); strings.TrimSpace(p) != "" {
		t.Errorf("poll after the session ended: %q", p)
	}
	ev := h.Events("routerscript.agent_session_closed")
	if len(ev) != 1 || ev[0].Payload["reason"] != "revoked" || ev[0].Actor != events.MemberActor(1) {
		t.Fatalf("closed: %+v", ev)
	}
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i+len(from):]
	j := strings.Index(s, to)
	if j < 0 {
		return ""
	}
	return s[:j]
}

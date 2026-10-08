package agent_test

import (
	"bytes"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/agent"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

type draftJSON struct {
	Revision int    `json:"revision"`
	ETag     string `json:"etag"`
	Manifest string `json:"manifest"`
	Files    []struct {
		Path string `json:"path"`
		Size int    `json:"size"`
		ETag string `json:"etag"`
	} `json:"files"`
}

func (e *env) draft(token string) draftJSON {
	e.T.Helper()
	rec := e.get(token, "/draft")
	status(e.T, rec, http.StatusOK)
	var d draftJSON
	decode(e.T, rec, &d)
	return d
}

func (d draftJSON) etag(path string) string {
	for _, f := range d.Files {
		if f.Path == path {
			return f.ETag
		}
	}
	return ""
}

func (e *env) savedEvents() int {
	return len(e.H.Events("template.draft_saved"))
}

func TestAgentWriteNeedsETag(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	d := e.draft(op.Token)
	if d.etag(manifest.Name) == "" || d.Manifest == "" {
		t.Fatalf("draft: %+v", d)
	}
	if !strings.HasPrefix(d.etag(manifest.Name), `"`+strconv.Itoa(d.Revision)+"-") {
		t.Fatalf("file ETag %s does not start with the revision %d", d.etag(manifest.Name), d.Revision)
	}
	rec := e.get(op.Token, "/draft/files/"+manifest.Name)
	status(t, rec, http.StatusOK)
	if rec.Header().Get("ETag") != d.etag(manifest.Name) {
		t.Fatalf("file ETag %s, list ETag %s", rec.Header().Get("ETag"), d.etag(manifest.Name))
	}
	edited := append(rec.Body.Bytes(), []byte("\n# edited by the agent\n")...)
	before := e.savedEvents()

	put := func(etag string) int {
		hdr := map[string]string{}
		if etag != "" {
			hdr["If-Match"] = etag
		}
		return e.do(req{Method: "PUT", Path: "/draft/files/" + manifest.Name, Token: op.Token, Header: hdr, Body: edited}).Code
	}
	if code := put(""); code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", code)
	}
	if code := put(`"0-0000000000000000"`); code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", code)
	}
	if got := e.draft(op.Token); got.Revision != d.Revision || e.savedEvents() != before {
		t.Fatal("a refused write changed the draft")
	}

	rec = e.do(req{Method: "PUT", Path: "/draft/files/" + manifest.Name, Token: op.Token, Header: map[string]string{"If-Match": d.etag(manifest.Name)}, Body: edited})
	status(t, rec, http.StatusOK)
	after := e.draft(op.Token)
	if after.Revision != d.Revision+1 {
		t.Fatalf("revision %d, want %d", after.Revision, d.Revision+1)
	}
	if rec.Header().Get("ETag") != after.etag(manifest.Name) {
		t.Fatalf("answer ETag %s, draft says %s", rec.Header().Get("ETag"), after.etag(manifest.Name))
	}
	if !strings.Contains(after.Manifest, "# edited by the agent") {
		t.Fatal("the write is not in the draft")
	}

	evs := e.H.Events("template.draft_saved")
	if len(evs) != before+1 {
		t.Fatalf("draft_saved events %d, want %d", len(evs), before+1)
	}
	if ev := evs[0]; ev.Actor != "agent:"+strconv.FormatInt(op.SessionID, 10) || ev.Payload["by"] != "agent" {
		t.Fatalf("event: %+v", ev)
	}
	if got := e.session(op.SessionID); got.Saves != 1 {
		t.Fatalf("saves %d", got.Saves)
	}

	// The old ETag names a revision that is gone, whichever file it was for.
	if code := put(d.etag(manifest.Name)); code != http.StatusPreconditionFailed {
		t.Fatalf("an ETag of an old revision: %d", code)
	}
	// Writing the same bytes changes nothing: no revision, no event, no save.
	rec = e.do(req{Method: "PUT", Path: "/draft/files/" + manifest.Name, Token: op.Token, Header: map[string]string{"If-Match": after.etag(manifest.Name)}, Body: edited})
	status(t, rec, http.StatusOK)
	if again := e.draft(op.Token); again.Revision != after.Revision || e.savedEvents() != before+1 || e.session(op.SessionID).Saves != 1 {
		t.Fatal("saving the same content counted as a change")
	}
}

func TestAgentCreateAndDelete(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	path := "/draft/files/site/notes.txt"
	body := []byte("hello")

	if rec := e.do(req{Method: "PUT", Path: path, Token: op.Token, Body: body}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("create without If-None-Match: %d", rec.Code)
	}
	if rec := e.do(req{Method: "PUT", Path: path, Token: op.Token, Header: map[string]string{"If-Match": `"1-ab"`}, Body: body}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match on a file that does not exist: %d", rec.Code)
	}
	rec := e.do(req{Method: "PUT", Path: path, Token: op.Token, Header: map[string]string{"If-None-Match": "*"}, Body: body})
	status(t, rec, http.StatusCreated)
	d := e.draft(op.Token)
	etag := d.etag("site/notes.txt")
	if etag == "" {
		t.Fatal("the new file is not in the draft")
	}
	// Creating over an existing file is a conflict.
	if rec := e.do(req{Method: "PUT", Path: path, Token: op.Token, Header: map[string]string{"If-None-Match": "*"}, Body: body}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("create over an existing file: %d", rec.Code)
	}
	// A bad path is refused.
	for _, bad := range []string{"/draft/files/../etc/passwd", "/draft/files/a//b", "/draft/files/.git/config"} {
		rec := e.do(req{Method: "PUT", Path: bad, Token: op.Token, Header: map[string]string{"If-None-Match": "*"}, Body: body})
		if rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("PUT %s: %d", bad, rec.Code)
		}
	}

	if rec := e.do(req{Method: "DELETE", Path: path, Token: op.Token}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("delete without If-Match: %d", rec.Code)
	}
	if rec := e.do(req{Method: "DELETE", Path: path, Token: op.Token, Header: map[string]string{"If-Match": `"1-ab"`}}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("delete with a stale If-Match: %d", rec.Code)
	}
	status(t, e.do(req{Method: "DELETE", Path: path, Token: op.Token, Header: map[string]string{"If-Match": etag}}), http.StatusOK)
	if got := e.draft(op.Token); got.etag("site/notes.txt") != "" {
		t.Fatal("the file is still in the draft")
	}
	status(t, e.get(op.Token, "/draft/files/site/notes.txt"), http.StatusNotFound)
	if rec := e.do(req{Method: "DELETE", Path: path, Token: op.Token, Header: map[string]string{"If-Match": etag}}); rec.Code != http.StatusNotFound {
		t.Fatalf("delete of a missing file: %d", rec.Code)
	}
	if got := e.session(op.SessionID); got.Saves != 2 {
		t.Fatalf("saves %d, want 2 (create, delete)", got.Saves)
	}
}

func TestAgentScopeIsNarrow(t *testing.T) {
	e := newEnv(t)
	other, err := e.H.Mod.Templates.Create(bg, "Other", "other", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := e.H.Mod.Templates.Draft(bg, other)
	if err != nil {
		t.Fatal(err)
	}
	op := e.open(agent.Open{})
	own, _ := e.H.Mod.Templates.Draft(bg, e.Tpl)
	versions := func() int {
		v, err := e.H.Mod.Templates.Versions(bg, e.Tpl)
		if err != nil {
			t.Fatal(err)
		}
		return len(v)
	}
	nVersions := versions()
	id := strconv.FormatInt(e.Tpl, 10)

	// Paths of the admin and of the API that must not exist for a token. The
	// admin ones are behind a session cookie the token does not have.
	tid := strconv.FormatInt(other, 10)
	for _, c := range []req{
		{Method: "POST", Path: "/publish"},
		{Method: "POST", Path: "/draft/discard"},
		{Method: "DELETE", Path: "/draft"},
		{Method: "POST", Path: "/import"},
		{Method: "POST", Path: "/deploy"},
		{Method: "GET", Path: "/templates/" + tid + "/draft"},
		{Method: "PUT", Path: "/templates/" + tid + "/draft/files/manifest.yaml", Header: map[string]string{"If-None-Match": "*"}, Body: []byte("x")},
		{Method: "GET", Path: "/servers"},
	} {
		c.Token = op.Token
		if rec := e.do(c); rec.Code < 400 {
			t.Errorf("%s /agent/v1%s: %d", c.Method, c.Path, rec.Code)
		}
	}
	// The same token on the admin's own routes: no session, no way in.
	for _, c := range []struct{ method, path string }{
		{"POST", "/templates/" + id + "/publish"},
		{"POST", "/templates/" + id + "/draft/discard"},
		{"POST", "/templates/import"},
		{"POST", "/servers/new"},
		{"GET", "/templates/" + id + "/edit"},
	} {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+op.Token)
		rec := e.H.Site.Do(siteReq(c.method, c.path, h))
		if rec.Code >= 200 && rec.Code < 300 {
			t.Errorf("%s %s with a token: %d", c.method, c.path, rec.Code)
		}
		if loc := rec.Header().Get("Location"); rec.Code/100 == 3 && !strings.HasPrefix(loc, "/login") {
			t.Errorf("%s %s with a token redirected to %s", c.method, c.path, loc)
		}
	}

	// Nothing changed: the draft is the same revision, nothing was published,
	// the other template's draft is as it was, the session is still alive.
	now, _ := e.H.Mod.Templates.Draft(bg, e.Tpl)
	if now.Revision != own.Revision || versions() != nVersions {
		t.Fatal("a refused request changed the template")
	}
	otherNow, err := e.H.Mod.Templates.Draft(bg, other)
	if err != nil || otherNow.Revision != otherBefore.Revision || len(otherNow.Files) != len(otherBefore.Files) {
		t.Fatalf("the other template's draft changed: %v", err)
	}
	status(t, e.get(op.Token, "/draft"), http.StatusOK)

	// And a token of one template never reaches another's files by name.
	e.advance(time.Second)
	if rec := e.get(op.Token, "/draft/files/nope.txt"); rec.Code != http.StatusNotFound {
		t.Fatalf("a missing file: %d", rec.Code)
	}
}

func TestAgentPreviewIsMasked(t *testing.T) {
	e := newEnv(t)
	sid := e.H.Provisioned()
	if e.H.Server(sid).State != "active" {
		t.Fatalf("server not active: %s", e.H.Server(sid).FailedError)
	}
	name := e.H.Server(sid).Name
	op := e.open(agent.Open{ServerIDs: []int64{sid}})

	// A server that was not chosen: forbidden, whether it exists or not.
	for _, other := range []string{"nl-99", "xx-1", ""} {
		if rec := e.do(req{Method: "POST", Path: "/draft/preview?server=" + other, Token: op.Token}); rec.Code != http.StatusForbidden {
			t.Fatalf("preview of %q: %d", other, rec.Code)
		}
	}
	rec := e.do(req{Method: "POST", Path: "/draft/preview?server=" + name, Token: op.Token})
	status(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, "•••") {
		t.Fatalf("nothing is masked in the preview:\n%.600s", body)
	}
	if !strings.Contains(body, e.H.Server(sid).ProxyHostname) {
		t.Fatalf("the preview is not about %s:\n%.600s", name, body)
	}

	// The real values of the server.
	rows, err := store.GeneratedValues(bg, e.H.App.DB.R, sid)
	if err != nil {
		t.Fatal(err)
	}
	real, err := sealed.OpenGenerated(e.H.App.Vault, sid, rows)
	if err != nil || len(real) == 0 {
		t.Fatalf("generated values: %v %v", real, err)
	}
	for k, v := range real {
		if v != "" && strings.Contains(body, v) {
			t.Fatalf("the preview holds the generated value %q", k)
		}
	}
	// The context lists the preview by name.
	var cx struct {
		Previews []string `json:"previews"`
	}
	decode(t, e.get(op.Token, "/context"), &cx)
	if len(cx.Previews) != 1 || cx.Previews[0] != name {
		t.Fatalf("previews: %v", cx.Previews)
	}
}

func TestAgentContextLogIsRedacted(t *testing.T) {
	e := newEnv(t)
	sid := e.H.Provisioned()
	job := e.H.Job(sid)
	stored := e.H.Log(job.ID)
	if strings.TrimSpace(stored) == "" {
		t.Fatal("the provisioning job has no log")
	}
	op := e.open(agent.Open{JobID: job.ID, IncludeReport: true, IncludeDiff: true})

	rec := e.get(op.Token, "/context")
	status(t, rec, http.StatusOK)
	var cx struct {
		Problem string `json:"problem"`
		Report  *struct {
			Errors int `json:"errors"`
		} `json:"report"`
		Job *struct {
			ID     int64    `json:"id"`
			Server string   `json:"server"`
			Log    []string `json:"log"`
		} `json:"failed_job"`
		Diff  []map[string]any  `json:"diff"`
		Links map[string]string `json:"links"`
	}
	decode(t, rec, &cx)
	if cx.Job == nil || cx.Job.ID != job.ID || len(cx.Job.Log) == 0 {
		t.Fatalf("no job log in the context: %s", rec.Body)
	}
	if cx.Report == nil || cx.Problem == "" || cx.Links["draft"] == "" || cx.Links["docs_manifest"] == "" {
		t.Fatalf("context: %s", rec.Body)
	}
	if cx.Diff == nil {
		t.Fatalf("no diff in the context: %s", rec.Body)
	}

	// It is the stored log, which the job system redacted: no secret of the
	// job or of the server is in it.
	secrets := map[string]string{}
	for k, v := range e.H.JobSecrets(job.ID) {
		secrets["job secret "+k] = v
	}
	rows, _ := store.GeneratedValues(bg, e.H.App.DB.R, sid)
	gen, _ := sealed.OpenGenerated(e.H.App.Vault, sid, rows)
	for k, v := range gen {
		secrets["generated "+k] = v
	}
	if len(secrets) == 0 {
		t.Fatal("the test has no secret to look for")
	}
	for what, v := range secrets {
		if len(v) >= 6 && strings.Contains(rec.Body.String(), v) {
			t.Fatalf("the context holds the %s", what)
		}
	}
	for _, line := range cx.Job.Log {
		if !strings.Contains(stored, strings.TrimSpace(regexp.MustCompile(`^\[[^\]]*\] `).ReplaceAllString(line, ""))) {
			t.Fatalf("line %q is not in the stored log", line)
		}
	}

	// A job of another template's server (or no server) cannot be offered.
	if _, err := e.Ag.OpenSession(bg, agent.Open{TemplateID: e.Tpl, Agent: "other", Problem: "x", JobID: 999999}, "admin"); err == nil {
		t.Fatal("an unknown job was accepted")
	}
	other, _ := e.H.Mod.Templates.Create(bg, "Other", "other", "", "admin")
	if _, err := e.Ag.OpenSession(bg, agent.Open{TemplateID: other, Agent: "other", Problem: "x", JobID: job.ID}, "admin"); err == nil {
		t.Fatal("a job of another template's server was accepted")
	}
}

func TestAgentRateLimits(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{For: 8 * time.Hour})

	for i := 1; i <= 60; i++ {
		if rec := e.get(op.Token, "/draft"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	rec := e.get(op.Token, "/draft")
	status(t, rec, http.StatusTooManyRequests)
	if n, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || n < 1 || n > 61 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	// A refused request is not counted: the window opens again after a minute.
	e.advance(61 * time.Second)
	status(t, e.get(op.Token, "/draft"), http.StatusOK)
	e.advance(61 * time.Second)

	// 600 in all, however slowly.
	used := e.session(op.SessionID).Requests
	for used < 600 {
		for i := 0; i < 60 && used < 600; i++ {
			status(t, e.get(op.Token, "/draft"), http.StatusOK)
			used++
		}
		e.advance(61 * time.Second)
	}
	rec = e.get(op.Token, "/draft")
	status(t, rec, http.StatusTooManyRequests)
	if got := e.session(op.SessionID).Requests; got != 600 {
		t.Fatalf("counted %d requests", got)
	}
}

func TestAgentAPIIgnoresCookies(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	h := http.Header{}

	// The admin's own session cookie is no credential here.
	rec := e.H.Site.Do(siteReq("GET", "/agent/v1/draft", h, e.H.Login.Cookie))
	ended(t, rec)
	if c := rec.Header().Get("Set-Cookie"); c != "" {
		t.Fatalf("Set-Cookie on a refusal: %s", c)
	}

	// With the token, the cookie changes nothing and nothing is set.
	h.Set("Authorization", "Bearer "+op.Token)
	rec = e.H.Site.Do(siteReq("GET", "/agent/v1/draft", h, e.H.Login.Cookie))
	status(t, rec, http.StatusOK)
	if c := rec.Header().Get("Set-Cookie"); c != "" {
		t.Fatalf("Set-Cookie: %s", c)
	}
	// No CSRF token needed, none asked for.
	rec = e.H.Site.Do(siteReq("POST", "/agent/v1/draft/validate", h, e.H.Login.Cookie))
	status(t, rec, http.StatusOK)
	if got := e.session(op.SessionID); got.Validations != 1 {
		t.Fatalf("validations %d", got.Validations)
	}
	if evs := e.H.Events("template.draft_validated"); len(evs) != 1 || evs[0].Payload["by"] != "agent" || evs[0].Actor != "agent:"+strconv.FormatInt(op.SessionID, 10) {
		t.Fatalf("validated events: %+v", evs)
	}

	// A malformed or foreign Authorization header is the same plain refusal.
	for _, bad := range []string{"", "Bearer", "Bearer nope", "Basic " + op.Token, "Bearer " + op.Token + "x", "Bearer " + strings.ToLower(op.Token)} {
		hh := http.Header{}
		if bad != "" {
			hh.Set("Authorization", bad)
		}
		ended(t, e.H.Site.Do(siteReq("GET", "/agent/v1/draft", hh)))
	}
}

func TestAgentSizeLimit(t *testing.T) {
	e := newEnv(t)
	op := e.open(agent.Open{})
	create := func(path string, n int) int {
		return e.do(req{Method: "PUT", Path: "/draft/files/" + path, Token: op.Token, Header: map[string]string{"If-None-Match": "*"}, Body: bytes.Repeat([]byte("a"), n)}).Code
	}
	if code := create("big.bin", 1<<20+1); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB + 1: %d", code)
	}
	if code := create("ok.bin", 1<<20); code != http.StatusCreated {
		t.Fatalf("1 MiB: %d", code)
	}
	// Ten more would take the draft over 10 MiB: one of them is refused.
	refused := false
	for i := 0; i < 10; i++ {
		if create("part"+strconv.Itoa(i)+".bin", 1<<20) == http.StatusRequestEntityTooLarge {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("the draft grew past 10 MiB")
	}
	d, err := e.H.Mod.Templates.Draft(bg, e.Tpl)
	if err != nil {
		t.Fatal(err)
	}
	size := 0
	for _, b := range d.Files {
		size += len(b)
	}
	if size > 10<<20 {
		t.Fatalf("draft is %d bytes", size)
	}
}

func TestManifestDocsInSync(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/modules/servers.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	from := strings.Index(doc, "### Manifest\n")
	to := strings.Index(doc, "### Future: per-link credentials")
	if from < 0 || to < from {
		t.Fatal("the manifest section of docs/modules/servers.md moved: update this test")
	}
	want := strings.TrimRight(doc[from:to], "\n") + "\n"
	if agent.ManifestDocs != want {
		t.Fatal("servers/agent/manifest.md differs from the manifest and rendering sections of docs/modules/servers.md: copy the section again")
	}

	e := newEnv(t)
	op := e.open(agent.Open{})
	rec := e.get(op.Token, "/docs/manifest")
	status(t, rec, http.StatusOK)
	if rec.Body.String() != want || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("served docs differ (%s)", rec.Header().Get("Content-Type"))
	}
}

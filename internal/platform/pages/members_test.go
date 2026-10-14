package pages_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

var setupLink = regexp.MustCompile(`data-copy="(http://proxier\.test/setup/[A-Za-z0-9_-]+)"`)

func levels(g access.Grant) url.Values {
	v := url.Values{}
	for _, a := range access.Areas() {
		v.Set("level."+string(a), string(g.Level(a)))
	}
	return v
}

// addThroughTheUI adds a member on Settings → Members and returns the setup
// link their page shows.
func addThroughTheUI(t *testing.T, l *sitetest.Login, username string, g access.Grant) (string, string) {
	t.Helper()
	form := levels(g)
	form.Set("username", username)
	form.Set("password", sitetest.Password)
	rec := l.Post("/settings/members", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add %s: %d %s", username, rec.Code, rec.Body.String())
	}
	page, _, _ := strings.Cut(rec.Header().Get("Location"), "?")
	m := setupLink.FindStringSubmatch(l.Get(page + "?added=1").Body.String())
	if m == nil {
		t.Fatalf("%s shows no setup link", page)
	}
	return page, m[1]
}

func setupPath(link string) string { return strings.TrimPrefix(link, "http://proxier.test") }

// The owner adds a member, who opens the setup link, chooses a password and
// is signed in with their grant.
func TestOwnerAddsAMemberThroughASetupLink(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	owner := s.SignIn("")
	page, link := addThroughTheUI(t, owner, "anna", access.PresetViewer.Grant())
	if !strings.Contains(owner.Get(page).Body.String(), "Waiting for setup") {
		t.Fatal("the new member isn't waiting for setup")
	}
	if !strings.Contains(owner.Get(page).Header().Get("Cache-Control"), "no-store") {
		t.Fatal("a page with a setup link may be kept")
	}

	path := setupPath(link)
	rec := s.Do(sitetest.Req{Path: path})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `value="anna"`) {
		t.Fatalf("setup page: %d", rec.Code)
	}
	rec = s.Do(sitetest.Req{Method: "POST", Path: path, Form: url.Values{"password": {"anna's own long password"}, "again": {"something else entirely"}}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "match") {
		t.Fatalf("mismatched passwords: %d", rec.Code)
	}
	rec = s.Do(sitetest.Req{Method: "POST", Path: path, Form: url.Values{"password": {"anna's own long password"}, "again": {"anna's own long password"}}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("setup: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	anna := &sitetest.Login{Site: s, Cookie: rec.Result().Cookies()[0]}
	if body := anna.Get("/").Body.String(); !strings.Contains(body, `title="anna"`) {
		t.Fatal("not signed in as anna")
	}
	// the link worked once
	if rec := s.Do(sitetest.Req{Path: path}); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "This link doesn&#39;t work") {
		t.Fatalf("used link: %d", rec.Code)
	}
	if rec := s.Do(sitetest.Req{Method: "POST", Path: path, Form: url.Values{"password": {"another long password"}, "again": {"another long password"}}}); rec.Code != http.StatusNotFound {
		t.Fatalf("used link, posted: %d", rec.Code)
	}
	if _, err := s.App.Auth.SignIn(t.Context(), "Anna", "anna's own long password", "192.0.2.9", ""); err != nil {
		t.Fatalf("anna's own password: %v", err)
	}
	if !strings.Contains(owner.Get("/settings/members").Body.String(), ">Viewer<") {
		t.Fatal("the list doesn't name anna's preset")
	}
}

func TestAddingNeedsThePasswordAndAFreeUsername(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	owner := s.SignIn("")
	form := levels(access.PresetViewer.Grant())
	form.Set("username", "anna")
	form.Set("password", "not my password")
	if rec := owner.Post("/settings/members", form); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Wrong password.") {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	form.Set("username", "ADMIN")
	form.Set("password", sitetest.Password)
	rec := owner.Post("/settings/members", form)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "That username is taken") || !strings.Contains(rec.Body.String(), `value="ADMIN"`) {
		t.Fatalf("taken username: %d", rec.Code)
	}
	if r, _ := s.App.Auth.Roster(t.Context()); len(r) != 1 {
		t.Fatalf("refused adds left %d members", len(r))
	}
	form.Set("level.servers", "everything")
	if rec := owner.Post("/settings/members", form); rec.Code != http.StatusBadRequest {
		t.Fatalf("a level that doesn't exist: %d", rec.Code)
	}
}

// A setup link opened in a browser signed in as someone else is refused:
// nobody sets up a member in their own session by accident.
func TestSetupLinkRefusesASignedInBrowser(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	owner := s.SignIn("")
	_, link := addThroughTheUI(t, owner, "anna", access.PresetViewer.Grant())
	rec := owner.Get(setupPath(link))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "signed in as admin") {
		t.Fatalf("signed in: %d", rec.Code)
	}
	rec = owner.Post(setupPath(link), url.Values{"password": {"anna's own long password"}, "again": {"anna's own long password"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("signed in, posted: %d", rec.Code)
	}
	if rec := s.Do(sitetest.Req{Path: setupPath(link)}); rec.Code != 200 {
		t.Fatalf("the refusal used the link up: %d", rec.Code)
	}
}

func TestExpiredSetupLinkIsRefusedAndReplaced(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	owner := s.SignIn("")
	page, link := addThroughTheUI(t, owner, "anna", access.PresetViewer.Grant())
	s.Advance(auth.SetupLinkTTL)
	if rec := s.Do(sitetest.Req{Path: setupPath(link)}); rec.Code != http.StatusNotFound {
		t.Fatalf("expired: %d", rec.Code)
	}
	body := owner.Get(page).Body.String()
	if !strings.Contains(body, "The setup link ran out") || setupLink.MatchString(body) {
		t.Fatal("an expired link is still offered")
	}
	if rec := owner.Post(page+"/setup-link", url.Values{"password": {sitetest.Password}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("new link: %d", rec.Code)
	}
	m := setupLink.FindStringSubmatch(owner.Get(page + "?done=link").Body.String())
	if m == nil || m[1] == link {
		t.Fatal("no new link")
	}
	if rec := s.Do(sitetest.Req{Path: setupPath(m[1])}); rec.Code != 200 {
		t.Fatalf("new link: %d", rec.Code)
	}
}

// A member with a limited grant sees only their areas, and a page of
// another area is 403: a page for a page, the key line's fragment for htmx.
func TestALimitedMemberSeesOnlyTheirAreas(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	anna := s.Join("anna", access.Grant{}.With(access.Subscriptions, access.LevelOwn), "")

	rec := anna.Get("/jobs")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Your access to Activity and jobs doesn&#39;t reach this page") {
		t.Fatalf("jobs: %d", rec.Code)
	}
	rec = anna.Get("/settings/members", http.Header{"Hx-Request": {"true"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `class="forbidden"`) || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("htmx: %d %s", rec.Code, rec.Body.String())
	}
	if rec := anna.Post("/settings/members", levels(access.Grant{})); rec.Code != http.StatusForbidden {
		t.Fatalf("adding without settings: %d", rec.Code)
	}

	home := anna.Get("/").Body.String()
	for _, hidden := range []string{`href="/jobs"`, `href="/activity"`, `id="jobs-cell"`, "Notifications are not configured", `data-goto="j"`} {
		if strings.Contains(home, hidden) {
			t.Errorf("the dashboard shows %s", hidden)
		}
	}
	settingsPage := anna.Get("/settings/general").Body.String()
	for _, hidden := range []string{`href="/settings/members"`, `href="/settings/integrations"`, `action="/settings/general"`} {
		if strings.Contains(settingsPage, hidden) {
			t.Errorf("General shows %s", hidden)
		}
	}
	if !strings.Contains(settingsPage, `action="/settings/general/language"`) || !strings.Contains(settingsPage, `href="/settings/security"`) {
		t.Error("General lacks the member's own language or Security")
	}
	if strings.Contains(anna.Get("/settings/security").Body.String(), `action="/settings/security/lockout"`) {
		t.Error("Security shows the lockout to a member without settings")
	}
	hits := anna.Get("/search?q=").Body.String()
	for _, hidden := range []string{"/jobs", "/activity", "/settings/members", "/settings/notifications"} {
		if strings.Contains(hits, `"`+hidden+`"`) {
			t.Errorf("search offers %s", hidden)
		}
	}
	if !strings.Contains(hits, `"/settings/security"`) {
		t.Error("search lacks the member's own Security")
	}
}

// Seeing a job isn't changing it: Cancel and Retry need the job's own area.
func TestCancellingAJobNeedsItsArea(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	e, err := s.App.Jobs.EnqueueNow(t.Context(), jobs.Request{Type: "platform.demo", CreatedBy: "system", Delay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	path := "/jobs/" + strconv.FormatInt(e.ID, 10)
	watcher := s.Join("watcher", access.Grant{}.With(access.Activity, access.LevelAll), "")
	if rec := watcher.Get(path); rec.Code != 200 || strings.Contains(rec.Body.String(), `data-action="job.cancel"`) {
		t.Fatalf("watching the job: %d, or offered Cancel", rec.Code)
	}
	if rec := watcher.Post(path+"/cancel", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Settings and members") {
		t.Fatalf("cancel without the job's area: %d", rec.Code)
	}
	if j, _ := s.App.Jobs.Job(t.Context(), e.ID); j.State != jobs.Queued {
		t.Fatalf("cancelled anyway: %s", j.State)
	}
	if rec := s.SignIn("").Post(path+"/cancel", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("cancel by the owner: %d", rec.Code)
	}
}

func TestNobodyChangesTheirOwnAccessOrRemovesTheLastSettingsManager(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	owner := s.SignIn("")
	body := owner.Get("/settings/members/1").Body.String()
	if !strings.Contains(body, "Nobody changes their own access") || !strings.Contains(body, `<fieldset class="access" disabled`) {
		t.Fatal("the owner's own access is offered for change")
	}
	if rec := owner.Post("/settings/members/1/access", levels(access.PresetViewer.Grant())); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("own access: %d", rec.Code)
	}
	rec := owner.Post("/settings/members/1/remove", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "last active member who manages Settings") {
		t.Fatalf("removing the last settings manager: %d", rec.Code)
	}
	if m, _ := s.App.Auth.Member(t.Context(), 1); m.Removed() {
		t.Fatal("removed anyway")
	}
	for _, p := range []string{"/settings/members", "/settings/members/1/access", "/settings/members/1/remove", "/settings/members/1/setup-link"} {
		rec := s.Do(sitetest.Req{Method: "POST", Path: p, Form: url.Values{}, Cookies: []*http.Cookie{owner.Cookie}})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without the CSRF token: %d", p, rec.Code)
		}
	}
}

// Options above the editor's own level are off, and a forged one is refused.
func TestALevelAboveYourOwnIsRefused(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	lim := s.Join("lim", access.Grant{}.With(access.Settings, access.LevelManage).With(access.Servers, access.LevelView), "")
	body := lim.Get("/settings/members/new").Body.String()
	if !regexp.MustCompile(`<option value="manage" disabled>Manage</option>`).MatchString(body) {
		t.Fatal("servers: manage is offered to a member at view")
	}
	form := levels(access.Grant{}.With(access.Servers, access.LevelManage))
	form.Set("username", "x")
	form.Set("password", sitetest.Password)
	rec := lim.Post("/settings/members", form)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "a level above your own") {
		t.Fatalf("above own: %d", rec.Code)
	}
}

// A removed member's session ends at once and Activity keeps their name.
func TestRemovingAMemberEndsTheirSessionAndKeepsTheirName(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	owner := s.SignIn("")
	anna := s.Join("anna", access.PresetOperator.Grant(), "")
	if rec := owner.Post("/settings/members/2/remove", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("remove: %d", rec.Code)
	}
	if rec := anna.Get("/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("anna's session after removal: %d", rec.Code)
	}
	list := owner.Get("/settings/members").Body.String()
	if !strings.Contains(list, ">Removed<") || !strings.Contains(list, ">anna<") {
		t.Fatal("the list doesn't keep anna, removed")
	}
	if act := owner.Get("/activity").Body.String(); !strings.Contains(act, `>anna</a>`) {
		t.Fatal("Activity doesn't name anna")
	}
}

// The form names every area, none included; the same levels saved again
// change nothing.
func TestSavingTheSameLevelsRecordsNothing(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	owner := s.SignIn("")
	s.Join("anna", access.PresetViewer.Grant(), "")
	if rec := owner.Post("/settings/members/2/access", levels(access.PresetViewer.Grant())); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", rec.Code)
	}
	var n int
	if err := s.App.DB.R.Get(&n, `SELECT COUNT(*) FROM events WHERE type = 'member.access_changed'`); err != nil || n != 0 {
		t.Fatalf("access_changed events: %d %v", n, err)
	}
	if rec := owner.Post("/settings/members/2/access", levels(access.PresetViewer.Grant().With(access.Servers, access.LevelManage))); rec.Code != http.StatusSeeOther {
		t.Fatalf("change: %d", rec.Code)
	}
	if err := s.App.DB.R.Get(&n, `SELECT COUNT(*) FROM events WHERE type = 'member.access_changed' AND json_extract(payload, '$.changes') = 'servers: view → manage'`); err != nil || n != 1 {
		t.Fatalf("the change: %d %v", n, err)
	}
}

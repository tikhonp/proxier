package pages_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// finishedJob makes a job of type platform.demo in state with n log lines.
func finishedJob(t *testing.T, s *sitetest.Site, state string, lines int) int64 {
	t.Helper()
	e, err := s.App.Jobs.EnqueueNow(t.Context(), jobs.Request{Type: "platform.demo", CreatedBy: "admin", Payload: map[string]any{"seconds": 3}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := s.App.DB.W.Exec(`UPDATE jobs SET state = ?, started_at = ?, finished_at = ?, attempt = 1, log_lines = ? WHERE id = ?`,
		state, now, now, lines, e.ID); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= lines; i++ {
		if _, err := s.App.DB.W.Exec(`INSERT INTO job_log_lines (job_id, seq, time, level, attempt, text) VALUES (?, ?, ?, 'info', 1, ?)`,
			e.ID, i, now, fmt.Sprintf("line %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	return e.ID
}

func TestJobStreamResumesFromLastEventID(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	id := finishedJob(t, s, "succeeded", 6)
	path := fmt.Sprintf("/jobs/%d/stream", id)

	full := l.Get(path)
	if full.Code != 200 || !strings.HasPrefix(full.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s", full.Code, full.Header().Get("Content-Type"))
	}
	body := full.Body.String()
	for i := 1; i <= 6; i++ {
		if !strings.Contains(body, fmt.Sprintf("line %d<", i)) {
			t.Fatalf("line %d missing:\n%s", i, body)
		}
	}
	if !strings.Contains(body, "event: done") {
		t.Fatal("no done event for a finished job")
	}
	// the page's "N lines" was rendered before the lines that streamed in
	if !strings.Contains(body, "event: info\ndata: redacted · 6 lines\n") {
		t.Fatalf("no line count event:\n%s", body)
	}
	// The ids are the line ids; reconnect after the third.
	var third string
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, "id: ") {
			if strings.Count(body[:strings.Index(body, ln)], "id: ") == 2 {
				third = strings.TrimPrefix(ln, "id: ")
			}
		}
	}
	rec := l.Site.Do(sitetest.Req{Path: path, Cookies: []*http.Cookie{l.Cookie}, Header: http.Header{"Last-Event-ID": {third}}})
	got := rec.Body.String()
	if strings.Contains(got, "line 3<") || strings.Contains(got, "line 1<") || !strings.Contains(got, "line 4<") || !strings.Contains(got, "line 6<") {
		t.Fatalf("resume from %s:\n%s", third, got)
	}
	if strings.Count(got, "event: line") != 3 {
		t.Fatalf("want 3 lines after the third, got:\n%s", got)
	}
}

func TestJobStreamLineCannotStartAField(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	id := finishedJob(t, s, "succeeded", 1)
	if _, err := s.App.DB.W.Exec(`UPDATE job_log_lines SET text = ? WHERE job_id = ?`,
		"ok\revent: done\rretry: 1\r\nid: 99", id); err != nil {
		t.Fatal(err)
	}
	body := l.Get(fmt.Sprintf("/jobs/%d/stream", id)).Body.String()
	if strings.Contains(body, "\r") {
		t.Fatalf("a carriage return reached the stream:\n%q", body)
	}
	for _, field := range []string{"event: done", "retry: 1", "id: 99"} {
		if !strings.Contains(body, "data: "+field) {
			t.Fatalf("%q is not data:\n%s", field, body)
		}
	}
	if strings.Count(body, "\nevent: done\n") != 1 {
		t.Fatalf("a second done event:\n%s", body)
	}
}

func TestJobsCellCounts(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	finishedJob(t, s, "running", 0)
	finishedJob(t, s, "failed", 0)
	cell := l.Get("/jobs/cell").Body.String()
	if !strings.Contains(cell, "1 job running") || !strings.Contains(cell, "1 failed") {
		t.Fatalf("cell:\n%s", cell)
	}
	if rec := l.Get("/jobs"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Demo job") {
		t.Fatalf("jobs page %d", rec.Code)
	}
	cell = l.Get("/jobs/cell").Body.String()
	if strings.Contains(cell, "failed") {
		t.Fatalf("visiting /jobs should reset the failed count:\n%s", cell)
	}
	if !strings.Contains(cell, "1 job running") {
		t.Fatalf("running count lost:\n%s", cell)
	}
	// a failure after the look shows again
	time.Sleep(5 * time.Millisecond)
	finishedJob(t, s, "failed", 0)
	if !strings.Contains(l.Get("/jobs/cell").Body.String(), "1 failed") {
		t.Fatal("a new failure should show")
	}
}

func TestCancelAndRetryActions(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	e, _ := s.App.Jobs.EnqueueNow(t.Context(), jobs.Request{Type: "platform.demo", CreatedBy: "admin", Delay: time.Hour})
	cancel := fmt.Sprintf("/jobs/%d/cancel", e.ID)

	noCSRF := s.Do(sitetest.Req{Method: "POST", Path: cancel, Cookies: []*http.Cookie{l.Cookie}, Form: url.Values{}})
	if noCSRF.Code != 403 {
		t.Fatalf("without CSRF: %d", noCSRF.Code)
	}
	if j, _ := s.App.Jobs.Job(t.Context(), e.ID); j.State != jobs.Queued {
		t.Fatalf("state %s after a refused cancel", j.State)
	}
	rec := l.Post(cancel, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != fmt.Sprintf("/jobs/%d", e.ID) {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if j, _ := s.App.Jobs.Job(t.Context(), e.ID); j.State != jobs.Cancelled {
		t.Fatalf("state %s", j.State)
	}
	rec = l.Post(fmt.Sprintf("/jobs/%d/retry", e.ID), nil)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || loc == fmt.Sprintf("/jobs/%d", e.ID) || !strings.HasPrefix(loc, "/jobs/") {
		t.Fatalf("retry: %d %s", rec.Code, loc)
	}
	page := l.Get(loc).Body.String()
	if !strings.Contains(page, "retry of #") {
		t.Fatalf("the new job should link to the old one:\n%s", page)
	}
	if l.Get("/jobs/9999").Code != 404 {
		t.Fatal("unknown job should be 404")
	}
}

func TestJobPageShowsLogAndLiveStream(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	done := finishedJob(t, s, "succeeded", 3)
	page := l.Get(fmt.Sprintf("/jobs/%d", done)).Body.String()
	if !strings.Contains(page, "line 3") || strings.Contains(page, "data-stream") {
		t.Fatalf("finished job page:\n%s", page)
	}
	// the log box scrolls on its own and can be expanded; a finished log has
	// nothing to follow
	if !strings.Contains(page, `id="log" class="log" tabindex="0"`) || !strings.Contains(page, `data-log-expand`) || strings.Contains(page, `data-log-follow`) {
		t.Fatalf("finished job page's log tools:\n%s", page)
	}
	live := finishedJob(t, s, "running", 2)
	page = l.Get(fmt.Sprintf("/jobs/%d", live)).Body.String()
	if !strings.Contains(page, `data-stream="/jobs/`) {
		t.Fatalf("running job page has no stream:\n%s", page)
	}
	if !strings.Contains(page, `data-log-follow checked`) || !strings.Contains(page, `data-log-expand`) {
		t.Fatalf("running job page has no Follow or Expand:\n%s", page)
	}
	txt := l.Get(fmt.Sprintf("/jobs/%d/log.txt", live))
	if txt.Code != 200 || !strings.Contains(txt.Body.String(), "line 2") {
		t.Fatalf("log.txt %d", txt.Code)
	}
}

func TestRunDemoJob(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	rec := l.Post("/jobs/demo", url.Values{"fail": {"1"}})
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/jobs/") {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	var _ = httptest.NewRecorder
}

// A job started by a member, before members ("admin") or since
// ("member:1"), names them by username on the Jobs page and the job page.
func TestJobsNameTheMemberWhoStartedThem(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	if _, err := s.App.DB.W.Exec(`UPDATE members SET username = 'tikhon' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	old := finishedJob(t, s, "succeeded", 0)
	e, err := s.App.Jobs.EnqueueNow(t.Context(), jobs.Request{Type: "platform.demo", CreatedBy: events.MemberActor(1), Delay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{old, e.ID} {
		if page := l.Get(fmt.Sprintf("/jobs/%d", id)).Body.String(); !strings.Contains(page, "started by tikhon") {
			t.Errorf("job %d doesn't name its member:\n%s", id, page)
		}
	}
	list := l.Get("/jobs").Body.String()
	if strings.Count(list, "tikhon") < 3 || strings.Contains(list, "member:1") || strings.Contains(list, "admin ·") {
		t.Errorf("the list shows a stored actor:\n%s", list)
	}
}

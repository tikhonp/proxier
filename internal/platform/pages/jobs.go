package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const (
	jobsPageSize = 50
	logPageSize  = 500
)

// jobsCellView counts for the header; a failed query shows nothing wrong.
func (h *handler) jobsCellView(c *echo.Context) templ.Component {
	ctx := c.Request().Context()
	seen, _ := h.Jobs.SeenAt(ctx)
	running, failed, err := h.Jobs.HeaderCounts(ctx, seen)
	if err != nil {
		h.Log.Error("pages: jobs cell", "error", err)
	}
	return ui.JobsCell(ui.JobsCellView{Running: running, Failed: failed})
}

func (h *handler) jobsCell(c *echo.Context) error {
	return web.Render(c, http.StatusOK, h.jobsCellView(c))
}

// stateKind maps a job state to a status marker and its word's i18n key.
func stateKind(st jobs.State) string {
	switch st {
	case jobs.Running:
		return "running"
	case jobs.Succeeded:
		return "ok"
	case jobs.Failed:
		return "broken"
	case jobs.Interrupted:
		return "look"
	case jobs.Cancelled:
		return "off"
	}
	return "unknown" // queued
}

// tr is T with a fallback for keys a module did not translate (job types,
// steps).
func tr(ctx context.Context, key, fallback string) string {
	if i18n.From(ctx).Has(key) {
		return i18n.T(ctx, key)
	}
	return fallback
}

func typeLabel(ctx context.Context, typ string) string { return tr(ctx, "job."+typ, typ) }

func stepLabel(ctx context.Context, typ, step string) string {
	return tr(ctx, "job."+typ+".step."+step, step)
}

// byLabel turns "admin", "schedule:platform.retention", "event:12" into words.
func byLabel(ctx context.Context, by string) string {
	kind, rest, _ := strings.Cut(by, ":")
	label := tr(ctx, "jobs.by."+kind, kind)
	if rest != "" && kind != "event" {
		return label + " · " + rest
	}
	return label
}

func stateWord(ctx context.Context, j jobs.Job) string {
	return i18n.T(ctx, "jobs.state."+string(j.State))
}

type jobRowView struct {
	ID      int64
	Href    string
	Type    string
	By      string
	Subject ui.SubjectRef
	Kind    string
	State   string
	Detail  string
	Started string
	Took    string
	Hint    string
}

type jobsView struct {
	Rows     []jobRowView
	Chips    []ui.Chip
	Form     *ui.FilterForm
	Clear    string
	Info     string
	Older    string
	Total    int
	Subtitle string
}

type jobFilter struct {
	State      string
	Type       string
	Subject    string
	HideChecks bool
	Before     int64
}

func parseJobFilter(c *echo.Context) jobFilter {
	q := c.Request().URL.Query()
	f := jobFilter{State: q.Get("state"), Type: q.Get("type"), Subject: q.Get("subject"), HideChecks: q.Get("hide_checks") == "1"}
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	switch f.State {
	case "queued", "running", "failed", "succeeded", "cancelled", "interrupted":
	default:
		f.State = ""
	}
	return f
}

func (f jobFilter) query() url.Values {
	v := url.Values{}
	if f.State != "" {
		v.Set("state", f.State)
	}
	if f.Type != "" {
		v.Set("type", f.Type)
	}
	if f.Subject != "" {
		v.Set("subject", f.Subject)
	}
	if f.HideChecks {
		v.Set("hide_checks", "1")
	}
	return v
}

func (f jobFilter) href(mut func(*jobFilter)) string {
	g := f
	g.Before = 0
	mut(&g)
	if q := g.query().Encode(); q != "" {
		return "/jobs?" + q
	}
	return "/jobs"
}

func (f jobFilter) active() bool {
	return f.State != "" || f.Type != "" || f.Subject != "" || f.HideChecks
}

func (h *handler) jobsPage(c *echo.Context) error {
	ctx := c.Request().Context()
	f := parseJobFilter(c)
	filter := jobs.Filter{Type: f.Type, HideChecks: f.HideChecks, Before: f.Before, Limit: jobsPageSize + 1}
	if f.State != "" {
		filter.States = []jobs.State{jobs.State(f.State)}
	}
	if typ, id, ok := strings.Cut(f.Subject, ":"); ok {
		filter.Subject = events.Subject{Type: typ, ID: id}
	}
	list, err := h.Jobs.List(ctx, filter)
	if err != nil {
		return err
	}
	older := ""
	if len(list) > jobsPageSize {
		list = list[:jobsPageSize]
		o := f
		o.Before = list[len(list)-1].ID
		older = "/jobs?" + o.queryWithBefore()
	}

	// Looking at the list is "seeing" the failures; the counts for the chips
	// and the header are taken first.
	seen, _ := h.Jobs.SeenAt(ctx)
	running, failedSince, _ := h.Jobs.HeaderCounts(ctx, seen)
	if f.Before == 0 {
		if err := h.Jobs.MarkSeen(ctx); err != nil {
			h.Log.Error("pages: mark jobs seen", "error", err)
		}
	}

	subs := make([]events.Subject, len(list))
	for i, j := range list {
		subs[i] = j.Subject
	}
	named := h.names.resolve(ctx, subs)
	loc := i18n.From(ctx)
	v := jobsView{Older: older}
	for _, j := range list {
		row := jobRowView{
			ID: j.ID, Href: "/jobs/" + strconv.FormatInt(j.ID, 10), Type: typeLabel(ctx, j.Type), By: byLabel(ctx, j.CreatedBy),
			Subject: named[j.Subject], Kind: stateKind(j.State), State: stateWord(ctx, j), Started: "—", Took: "—",
		}
		row.Detail = h.jobDetail(ctx, j)
		if !j.StartedAt.IsZero() {
			row.Started = loc.Clock(j.StartedAt.Time)[:5]
			if time.Since(j.StartedAt.Time) > 20*time.Hour {
				row.Started = loc.Day(j.StartedAt.Time)
			}
		}
		if !j.StartedAt.IsZero() && !j.FinishedAt.IsZero() {
			row.Took = loc.Duration(j.FinishedAt.Sub(j.StartedAt.Time))
		}
		row.Hint = i18n.T(ctx, "jobs.hint", i18n.Args{"id": j.ID})
		v.Rows = append(v.Rows, row)
	}

	v.Chips = []ui.Chip{
		{Label: i18n.T(ctx, "jobs.filter.any"), Href: f.href(func(g *jobFilter) { g.State = "" }), On: f.State == ""},
		{Label: i18n.T(ctx, "jobs.filter.running", i18n.Args{"n": int64(running)}), Href: f.href(func(g *jobFilter) { g.State = "running" }), On: f.State == "running"},
		{Label: i18n.T(ctx, "jobs.filter.failed", i18n.Args{"n": int64(failedSince)}), Href: f.href(func(g *jobFilter) { g.State = "failed" }), On: f.State == "failed"},
		{Label: i18n.T(ctx, "jobs.filter.hide_checks"), Href: f.href(func(g *jobFilter) { g.HideChecks = !g.HideChecks }), On: f.HideChecks},
	}
	typeOpts := []ui.FilterOption{{Value: "", Label: i18n.T(ctx, "jobs.filter.type_any")}}
	for _, t := range h.Jobs.Types() {
		typeOpts = append(typeOpts, ui.FilterOption{Value: t, Label: typeLabel(ctx, t)})
	}
	hidden := map[string]string{}
	for k, vals := range f.query() {
		if k != "type" {
			hidden[k] = vals[0]
		}
	}
	v.Form = &ui.FilterForm{Action: "/jobs", Hidden: hidden, Apply: i18n.T(ctx, "jobs.filter.apply"),
		Selects: []ui.FilterSelect{{Name: "type", Label: i18n.T(ctx, "jobs.filter.type"), Value: f.Type, Options: typeOpts}}}
	if f.active() {
		v.Clear = "/jobs"
	}
	v.Info = i18n.N(ctx, "jobs.shown", int64(len(v.Rows)))
	return web.Render(c, http.StatusOK, jobsPage(h.shell(c, i18n.T(ctx, "nav.jobs"), "/jobs"), v))
}

func (f jobFilter) queryWithBefore() string {
	q := f.query()
	if f.Before > 0 {
		q.Set("before", strconv.FormatInt(f.Before, 10))
	}
	return q.Encode()
}

// jobDetail is the short line after the state word in the list.
func (h *handler) jobDetail(ctx context.Context, j jobs.Job) string {
	switch j.State {
	case jobs.Running:
		if steps, err := h.Jobs.Steps(ctx, j.ID); err == nil {
			for i, s := range steps {
				if s.State == "running" {
					return i18n.T(ctx, "jobs.detail.step", i18n.Args{"i": i + 1, "n": len(steps), "name": stepLabel(ctx, j.Type, s.Name)})
				}
			}
		}
	case jobs.Failed:
		if j.ErrorStep != "" {
			return i18n.T(ctx, "jobs.detail.failed_at", i18n.Args{"step": stepLabel(ctx, j.Type, j.ErrorStep), "error": firstLine(j.Error)})
		}
		return firstLine(j.Error)
	case jobs.Queued:
		if j.Attempt > 0 {
			return i18n.T(ctx, "jobs.detail.retry_at", i18n.Args{"time": i18n.From(ctx).Clock(j.RunAfter.Time)[:5], "n": j.Attempt, "max": j.MaxAttempts})
		}
		if j.RunAfter.After(time.Now().Add(5 * time.Second)) {
			return i18n.T(ctx, "jobs.detail.waits", i18n.Args{"time": i18n.From(ctx).Clock(j.RunAfter.Time)[:5]})
		}
	case jobs.Cancelled:
		if j.CancelRequestedBy != "" {
			return i18n.T(ctx, "jobs.detail.cancelled_by", i18n.Args{"by": j.CancelRequestedBy})
		}
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if r := []rune(line); len(r) > 140 {
		return string(r[:140]) + "…"
	}
	return line
}

func (h *handler) runDemo(c *echo.Context) error {
	ctx := c.Request().Context()
	p := jobs.DemoPayload{Seconds: 30, Fail: c.FormValue("fail") == "1"}
	e, err := h.Jobs.EnqueueNow(ctx, jobs.Request{Type: "platform.demo", Payload: p, CreatedBy: events.ActorAdmin})
	if err != nil {
		return err
	}
	return web.Redirect(c, "/jobs/"+strconv.FormatInt(e.ID, 10))
}

type stepView struct {
	Name  string
	Kind  string // status marker kind
	State string
	Time  string
	Error string
}

type kv struct{ Key, Value string }

func (v jobView) stepsUI() []ui.JobStep {
	out := make([]ui.JobStep, len(v.Steps))
	for i, s := range v.Steps {
		out[i] = ui.JobStep(s)
	}
	return out
}

func (v jobView) logLinesUI() []ui.LogLine {
	out := make([]ui.LogLine, len(v.Lines))
	for i, l := range v.Lines {
		out[i] = ui.LogLine(l)
	}
	return out
}

// stream is the live stream's address, empty for a job that has ended.
func (v jobView) stream() string {
	if !v.Live {
		return ""
	}
	return "/jobs/" + strconv.FormatInt(v.ID, 10) + "/stream?after=" + strconv.FormatInt(v.LastID, 10)
}

type jobView struct {
	ID       int64
	Title    string
	Type     string
	Kind     string
	State    string
	Note     string
	Meta     string
	Subject  ui.SubjectRef
	RetryOf  int64
	Steps    []stepView
	StepsHdr string
	Attempt  []string
	Payload  []kv
	Lines    []logLineView
	LastID   int64
	Live     bool // the stream is worth opening
	Earlier  string
	CanStop  bool
	CanRetry bool
	Retried  bool // queued for a retry: Retry now / Cancel the retries
	Error    string
	LogInfo  string
}

type logLineView struct {
	ID    int64
	Time  string
	Level string
	Text  string
}

func isTerminal(st jobs.State) bool {
	return st == jobs.Succeeded || st == jobs.Failed || st == jobs.Cancelled
}

func (h *handler) buildJob(ctx context.Context, j jobs.Job) (jobView, error) {
	loc := i18n.From(ctx)
	steps, err := h.Jobs.Steps(ctx, j.ID)
	if err != nil {
		return jobView{}, err
	}
	named := h.names.resolve(ctx, []events.Subject{j.Subject})
	v := jobView{
		ID: j.ID, Type: j.Type, Kind: stateKind(j.State), State: stateWord(ctx, j), Subject: named[j.Subject], RetryOf: j.RetryOf,
		Title: typeLabel(ctx, j.Type), Error: j.Error,
	}
	if j.Subject.Type != "" {
		v.Title += " · " + v.Subject.Label
	}
	if j.State == jobs.Queued && j.Attempt > 0 {
		v.Retried = true
		v.Note = i18n.T(ctx, "job.next_attempt", i18n.Args{"time": loc.Clock(j.RunAfter.Time)[:5]})
		v.State = i18n.T(ctx, "jobs.state.failed_attempt", i18n.Args{"n": j.Attempt, "max": j.MaxAttempts})
		v.Kind = "broken"
	}
	meta := []string{"#" + strconv.FormatInt(j.ID, 10), i18n.T(ctx, "job.queue", i18n.Args{"queue": string(j.Queue)}),
		i18n.T(ctx, "job.started_by", i18n.Args{"by": byLabel(ctx, j.CreatedBy)})}
	if j.ResourceKey != "" {
		meta = append(meta, i18n.T(ctx, "job.resource", i18n.Args{"key": j.ResourceKey}))
	}
	v.Meta = strings.Join(meta, " · ")
	for _, s := range steps {
		sv := stepView{Name: stepLabel(ctx, j.Type, s.Name), Error: s.Error}
		switch s.State {
		case "succeeded":
			sv.Kind = "ok"
		case "running":
			sv.Kind = "running"
		case "failed":
			sv.Kind = "broken"
		case "cancelled":
			sv.Kind = "off"
		default:
			sv.Kind = "unknown"
		}
		sv.State = s.State
		switch {
		case s.CarriedFrom != 0:
			sv.Time = i18n.T(ctx, "job.carried", i18n.Args{"id": s.CarriedFrom})
		case !s.StartedAt.IsZero() && !s.FinishedAt.IsZero():
			sv.Time = loc.Duration(s.FinishedAt.Sub(s.StartedAt.Time))
		}
		v.Steps = append(v.Steps, sv)
	}
	v.StepsHdr = i18n.T(ctx, "job.steps_attempt", i18n.Args{"n": max(j.Attempt, 1)})
	v.Attempt = h.attempts(ctx, j)
	v.Payload = payloadRows(j)
	v.CanStop = j.State == jobs.Queued || j.State == jobs.Running || j.State == jobs.Interrupted
	v.CanRetry = j.State == jobs.Failed || j.State == jobs.Cancelled
	if j.State == jobs.Running && j.CancelRequestedBy != "" {
		v.Note = i18n.T(ctx, "job.cancelling", i18n.Args{"by": j.CancelRequestedBy})
		v.CanStop = false
	}
	v.Live = !isTerminal(j.State)
	v.LogInfo = i18n.N(ctx, "job.log_info", int64(j.LogLines))
	return v, nil
}

func (h *handler) attempts(ctx context.Context, j jobs.Job) []string {
	loc := i18n.From(ctx)
	var out []string
	if j.Attempt > 0 || j.State == jobs.Failed {
		line := i18n.T(ctx, "job.attempt_line", i18n.Args{"n": max(j.Attempt, 1), "max": j.MaxAttempts})
		if j.Error != "" {
			line += " · " + firstLine(j.Error)
		}
		out = append(out, line)
	}
	if t, ok := h.Jobs.TypeInfo(j.Type); ok && len(t.Backoff) > 0 && j.MaxAttempts > 1 {
		parts := make([]string, len(t.Backoff))
		for i, d := range t.Backoff {
			parts[i] = loc.Duration(d)
		}
		out = append(out, i18n.T(ctx, "job.backoff", i18n.Args{"list": strings.Join(parts, ", ")}))
	}
	if j.State == jobs.Queued && !j.RunAfter.IsZero() && j.Attempt == 0 && j.RunAfter.After(time.Now().Add(5*time.Second)) {
		out = append(out, i18n.T(ctx, "jobs.detail.waits", i18n.Args{"time": loc.Clock(j.RunAfter.Time)[:5]}))
	}
	return out
}

func payloadRows(j jobs.Job) []kv {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(j.Payload, &m)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		var s string
		if err := json.Unmarshal(m[k], &s); err != nil {
			s = string(m[k])
		}
		if r := []rune(s); len(r) > 160 {
			s = string(r[:160]) + "…"
		}
		out = append(out, kv{k, s})
	}
	return out
}

func lineViews(ctx context.Context, lines []jobs.LogLine) []logLineView {
	loc := i18n.From(ctx)
	out := make([]logLineView, len(lines))
	for i, l := range lines {
		out[i] = logLineView{ID: l.ID, Time: loc.Clock(l.Time.Time), Level: l.Level, Text: l.Text}
	}
	return out
}

func jobID(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

func (h *handler) loadJob(c *echo.Context) (jobs.Job, error) {
	id, err := jobID(c)
	if err != nil {
		return jobs.Job{}, err
	}
	j, err := h.Jobs.Job(c.Request().Context(), id)
	if errors.Is(err, jobs.ErrNotFound) {
		return jobs.Job{}, echo.ErrNotFound
	}
	return j, err
}

func (h *handler) jobPage(c *echo.Context) error {
	ctx := c.Request().Context()
	j, err := h.loadJob(c)
	if err != nil {
		return err
	}
	v, err := h.buildJob(ctx, j)
	if err != nil {
		return err
	}
	var lines []jobs.LogLine
	if c.QueryParam("all") == "1" {
		lines, err = h.Jobs.LogAfter(ctx, j.ID, 0, 20000)
	} else {
		lines, err = h.Jobs.LogTail(ctx, j.ID, logPageSize)
		if err == nil && j.LogLines-j.LogDropped > len(lines) {
			v.Earlier = "/jobs/" + strconv.FormatInt(j.ID, 10) + "?all=1"
		}
	}
	if err != nil {
		return err
	}
	v.Lines = lineViews(ctx, lines)
	if len(lines) > 0 {
		v.LastID = lines[len(lines)-1].ID
	}
	return web.Render(c, http.StatusOK, jobPage(h.shell(c, fmt.Sprintf("#%d", j.ID), "/jobs"), v))
}

func (h *handler) jobLogText(c *echo.Context) error {
	ctx := c.Request().Context()
	j, err := h.loadJob(c)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	var after int64
	loc := i18n.From(ctx)
	for {
		lines, err := h.Jobs.LogAfter(ctx, j.ID, after, 1000)
		if err != nil {
			return err
		}
		for _, l := range lines {
			fmt.Fprintf(&b, "%s %s\n", loc.Clock(l.Time.Time), l.Text)
			after = l.ID
		}
		if len(lines) < 1000 {
			break
		}
	}
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="job-%d.log"`, j.ID))
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", b.Bytes())
}

func (h *handler) jobCancel(c *echo.Context) error {
	id, err := jobID(c)
	if err != nil {
		return err
	}
	if err := h.Jobs.Cancel(c.Request().Context(), id, events.ActorAdmin); err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			return echo.ErrNotFound
		}
		if !errors.Is(err, jobs.ErrNotActive) {
			return err
		}
	}
	return web.Redirect(c, "/jobs/"+strconv.FormatInt(id, 10))
}

// jobRetry retries a failed or cancelled job, or runs a job waiting for its
// backoff now.
func (h *handler) jobRetry(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := jobID(c)
	if err != nil {
		return err
	}
	j, err := h.Jobs.Job(ctx, id)
	if errors.Is(err, jobs.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	if j.State == jobs.Queued {
		if err := h.Jobs.RunNow(ctx, id); err != nil && !errors.Is(err, jobs.ErrNotActive) {
			return err
		}
		return web.Redirect(c, "/jobs/"+strconv.FormatInt(id, 10))
	}
	nid, err := h.Jobs.Retry(ctx, id, events.ActorAdmin)
	if err != nil {
		if errors.Is(err, jobs.ErrNotRetryable) {
			return web.Redirect(c, "/jobs/"+strconv.FormatInt(id, 10))
		}
		return err
	}
	return web.Redirect(c, "/jobs/"+strconv.FormatInt(nid, 10))
}

// jobStream is the live log: server-sent events that resume from the last line
// id (Last-Event-ID, or ?after= on a fresh connection).
func (h *handler) jobStream(c *echo.Context) error {
	ctx := c.Request().Context()
	j, err := h.loadJob(c)
	if err != nil {
		return err
	}
	if q := web.FromContext(ctx); q.Session != nil {
		ctx = ui.WithCSRF(ctx, q.Session.CSRF) // the head fragment holds action forms
	}
	after, _ := strconv.ParseInt(c.QueryParam("after"), 10, 64)
	if last, err := strconv.ParseInt(c.Request().Header.Get("Last-Event-ID"), 10, 64); err == nil && last > after {
		after = last
	}

	w := c.Response()
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	flush := func() error { return rc.Flush() }
	if err := flush(); err != nil {
		return nil
	}

	watch, stop := h.Jobs.Watch(j.ID)
	defer stop()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	var lastState jobs.State
	var lastSteps string
	for {
		j, err = h.Jobs.Job(ctx, j.ID)
		if err != nil {
			return nil
		}
		for {
			lines, err := h.Jobs.LogAfter(ctx, j.ID, after, logPageSize)
			if err != nil {
				return nil
			}
			for _, l := range lineViews(ctx, lines) {
				html, err := render(ctx, logLine(l))
				if err != nil {
					return nil
				}
				if err := writeEvent(w, fmt.Sprint(l.ID), "line", html); err != nil {
					return nil
				}
				after = l.ID
			}
			if len(lines) < logPageSize {
				break
			}
		}
		v, err := h.buildJob(ctx, j)
		if err != nil {
			return nil
		}
		if fragment, err := render(ctx, stepList(v)); err == nil && fragment != lastSteps {
			lastSteps = fragment
			if writeEvent(w, "", "steps", fragment) != nil {
				return nil
			}
		}
		if j.State != lastState {
			lastState = j.State
			if fragment, err := render(ctx, jobHead(v)); err == nil {
				if writeEvent(w, "", "state", fragment) != nil {
					return nil
				}
			}
		}
		if isTerminal(j.State) {
			// The state change and the last lines are in one flush, so nothing
			// is behind this point.
			_ = writeEvent(w, "", "done", string(j.State))
			_ = flush()
			return nil
		}
		if err := flush(); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-h.Closing:
			return nil
		case <-watch:
		case <-keepAlive.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return nil
			}
			_ = flush()
		}
	}
}

func render(ctx context.Context, c templ.Component) (string, error) {
	var b bytes.Buffer
	if err := c.Render(ctx, &b); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// writeEvent writes one server-sent event; data may span lines.
func writeEvent(w http.ResponseWriter, id, event, data string) error {
	var b strings.Builder
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("event: " + event + "\n")
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	_, err := w.Write([]byte(b.String()))
	return err
}

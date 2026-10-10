// Package agent hands a template draft to a coding agent
// (docs/processes/servers/template-authoring.md, "handing the draft to an
// agent"). Proxier never runs the agent: it gives the admin a prompt with a
// short-lived token that reaches one draft through /agent/v1 and nothing else.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// TokenPrefix starts every agent token.
const TokenPrefix = "pxa_"

// Durations of access. Anything longer than MaxDuration is capped.
const (
	DefaultDuration = 2 * time.Hour
	MaxDuration     = 8 * time.Hour
)

// Limits of one session.
const (
	MaxRequests      = 600
	MaxPerMinute     = 60
	MaxProblemRunes  = 4000
	maxLogLines      = 300
	maxLogBytes      = 64 << 10
	maxFailedOffered = 10
)

// Agents the prompt is worded for.
var Agents = []string{"claude-code", "codex", "other"}

var (
	ErrNoDraft   = templates.ErrNoDraft
	ErrNotFound  = store.ErrNotFound
	ErrNotOpen   = errors.New("the session is not open")
	ErrNoProblem = errors.New("say what the agent should do")
	ErrBadAgent  = errors.New("unknown agent")
	ErrBadJob    = errors.New("that job does not belong to a server of this template")
	ErrBadServer = errors.New("that server is not an active server of this template")
)

// Deps is what the service is built from.
type Deps struct {
	DB        *db.DB
	Events    *events.Catalog
	Vault     *vault.Vault
	Templates *templates.Service
	Jobs      *jobs.System
	Log       *slog.Logger
	// BaseURL is Proxier's own address, as the prompt shows it.
	BaseURL string
	// Preview builds the render context of a real server for a draft, with
	// every generated value and secret parameter masked (provision.Service's
	// PreviewContext with reveal false).
	Preview func(ctx context.Context, serverID int64) (func(m *manifest.Manifest, slug string) (render.Context, error), error)
}

// Service keeps the sessions.
type Service struct {
	// Now is the clock; tests move it.
	Now func() time.Time

	Deps
}

// New returns the service.
func New(d Deps) *Service { return &Service{Now: time.Now, Deps: d} }

func (s *Service) now() db.Time { return db.At(s.Now()) }

func subject(templateID int64) events.Subject {
	return events.Subject{Type: "template", ID: fmt.Sprint(templateID)}
}

// Open is what the admin chose in the hand-off dialog.
type Open struct {
	TemplateID    int64
	Problem       string
	Agent         string // claude-code codex other
	For           time.Duration
	IncludeReport bool
	JobID         int64   // 0 = none
	ServerIDs     []int64 // previews allowed
	IncludeDiff   bool
}

// Opened is the answer to opening a session. The token is shown once.
type Opened struct {
	SessionID int64
	Token     string
	Prompt    string
	ExpiresAt time.Time
}

// ReportSnapshot is the validation report as it was when the session opened.
type ReportSnapshot struct {
	At       time.Time         `json:"at"`
	Errors   int               `json:"errors"`
	Warnings int               `json:"warnings"`
	Findings []finding.Finding `json:"findings"`
}

// JobSnapshot is a failed job's stored (already redacted) log.
type JobSnapshot struct {
	ID        int64    `json:"id"`
	Type      string   `json:"type"`
	Server    string   `json:"server"`
	State     string   `json:"state"`
	ErrorStep string   `json:"error_step,omitempty"`
	Error     string   `json:"error,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Log       []string `json:"log"`
}

// Context is what the admin chose to show the agent, fixed at opening.
type Context struct {
	Report  *ReportSnapshot   `json:"report,omitempty"`
	Job     *JobSnapshot      `json:"job,omitempty"`
	Servers []store.ServerRef `json:"servers,omitempty"` // previews allowed
	Diff    bool              `json:"diff,omitempty"`
}

func parseContext(raw string) Context {
	var c Context
	_ = json.Unmarshal([]byte(raw), &c)
	return c
}

// FailedJob is a failed job of a server built from the template, offered in
// the dialog as context.
type FailedJob struct {
	ID         int64
	Type       string
	ServerName string
	Step       string
	Error      string
	At         time.Time
}

// FailedJobs lists the latest failed jobs of servers built from a template,
// newest first.
func (s *Service) FailedJobs(ctx context.Context, templateID int64) ([]FailedJob, error) {
	all, err := store.ListServers(ctx, s.DB.R)
	if err != nil {
		return nil, err
	}
	var out []FailedJob
	for _, sv := range all {
		if sv.TemplateID != templateID {
			continue
		}
		js, err := s.Jobs.List(ctx, jobs.Filter{States: []jobs.State{jobs.Failed}, Subject: store.ServerSubject(sv.ID), Limit: 3})
		if err != nil {
			return nil, err
		}
		for _, j := range js {
			out = append(out, FailedJob{ID: j.ID, Type: j.Type, ServerName: sv.Name, Step: j.ErrorStep, Error: j.Error, At: j.FinishedAt.Time})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > maxFailedOffered {
		out = out[:maxFailedOffered]
	}
	return out, nil
}

// OpenSession hands the template's draft to an agent. It closes the template's
// open session (reason replaced) in the same transaction, and returns the
// token and the prompt that holds it; neither is stored in a readable form.
func (s *Service) OpenSession(ctx context.Context, o Open, actor string) (Opened, error) {
	o.Problem = strings.TrimSpace(o.Problem)
	switch {
	case o.Problem == "" || utf8.RuneCountInString(o.Problem) > MaxProblemRunes:
		return Opened{}, ErrNoProblem
	case !contains(Agents, o.Agent):
		return Opened{}, ErrBadAgent
	}
	if o.For <= 0 {
		o.For = DefaultDuration
	}
	if o.For > MaxDuration {
		o.For = MaxDuration
	}
	t, err := store.GetTemplate(ctx, s.DB.R, o.TemplateID)
	if err != nil {
		return Opened{}, err
	}
	draft, err := s.Templates.Draft(ctx, o.TemplateID)
	if err != nil {
		return Opened{}, err
	}
	c, err := s.buildContext(ctx, o, draft)
	if err != nil {
		return Opened{}, err
	}
	raw, _ := json.Marshal(c)

	token := TokenPrefix + vault.NewToken()
	opened := s.now()
	expires := db.At(opened.Add(o.For))
	var id int64
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		// The partial unique index allows one open session per draft: end the
		// old one first. One whose time already passed ended by expiry, not by us.
		if old, err := store.OpenAgentSession(ctx, tx, o.TemplateID); err == nil {
			reason := "replaced"
			if !old.ExpiresAt.After(opened.Time) {
				reason = "expired"
			}
			if err := s.close(ctx, tx, old, reason, actor); err != nil {
				return err
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		id, err = store.InsertAgentSession(ctx, tx, store.AgentSession{
			TemplateID: o.TemplateID, TokenLookup: s.Vault.Lookup(token), Problem: o.Problem, Context: string(raw),
			Agent: o.Agent, OpenedAt: opened, ExpiresAt: expires,
		})
		if err != nil {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Type: "template.agent_session_opened", Subject: subject(o.TemplateID), Actor: actor,
			Payload: map[string]any{"expires_at": expires.Format(db.TimeLayout), "agent": o.Agent, "context": c.summary()}})
		return err
	})
	if err != nil {
		return Opened{}, err
	}
	return Opened{
		SessionID: id, Token: token, ExpiresAt: expires.Time,
		Prompt: Prompt(PromptData{Agent: o.Agent, Template: t.Name, Problem: o.Problem, BaseURL: s.BaseURL, Token: token, Expires: expires.Time, Context: c, BasedOn: draft.BasedOn}),
	}, nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// summary is what the opened event says was included, without the content.
func (c Context) summary() map[string]any {
	names := make([]string, len(c.Servers))
	for i, sv := range c.Servers {
		names[i] = sv.Name
	}
	m := map[string]any{"report": c.Report != nil, "diff": c.Diff, "servers": names}
	if c.Job != nil {
		m["job"] = c.Job.ID
	}
	return m
}

// buildContext computes what the dialog's checkboxes asked for. It reads;
// nothing is written.
func (s *Service) buildContext(ctx context.Context, o Open, d templates.Draft) (Context, error) {
	var c Context
	if o.IncludeReport {
		r, err := s.Templates.Report(ctx, o.TemplateID)
		if err != nil {
			return c, err
		}
		fs := r.Findings
		if fs == nil {
			fs = []finding.Finding{}
		}
		c.Report = &ReportSnapshot{At: s.Now().UTC(), Errors: len(r.Errors()), Warnings: len(r.Warnings()), Findings: fs}
	}
	if o.JobID > 0 {
		j, err := s.jobSnapshot(ctx, o.TemplateID, o.JobID)
		if err != nil {
			return c, err
		}
		c.Job = &j
	}
	seen := map[int64]bool{}
	for _, id := range o.ServerIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		sv, err := store.GetServer(ctx, s.DB.R, id)
		if err != nil || sv.TemplateID != o.TemplateID || sv.State != "active" || sv.Retiring() {
			return c, ErrBadServer
		}
		c.Servers = append(c.Servers, store.ServerRef{ID: sv.ID, Name: sv.Name})
	}
	c.Diff = o.IncludeDiff
	return c, nil
}

// jobSnapshot reads a job of a server built from the template. The stored log
// is already redacted by the job system.
func (s *Service) jobSnapshot(ctx context.Context, templateID, jobID int64) (JobSnapshot, error) {
	j, err := s.Jobs.Job(ctx, jobID)
	if err != nil || j.Subject.Type != "server" {
		return JobSnapshot{}, ErrBadJob
	}
	var sid int64
	if _, err := fmt.Sscan(j.Subject.ID, &sid); err != nil {
		return JobSnapshot{}, ErrBadJob
	}
	sv, err := store.GetServer(ctx, s.DB.R, sid)
	if err != nil || sv.TemplateID != templateID {
		return JobSnapshot{}, ErrBadJob
	}
	lines, err := s.Jobs.LogTail(ctx, jobID, maxLogLines)
	if err != nil {
		return JobSnapshot{}, err
	}
	snap := JobSnapshot{ID: j.ID, Type: j.Type, Server: sv.Name, State: string(j.State), ErrorStep: j.ErrorStep, Error: j.Error, Truncated: j.LogLines > len(lines)}
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		text := strings.TrimRight(lines[i].Text, "\n")
		if lines[i].Step != "" {
			text = "[" + lines[i].Step + "] " + text
		}
		size += len(text) + 1
		if size > maxLogBytes {
			snap.Truncated = true
			break
		}
		snap.Log = append([]string{text}, snap.Log...)
	}
	if snap.Log == nil {
		snap.Log = []string{}
	}
	return snap, nil
}

// close ends an open session in tx and records template.agent_session_closed.
func (s *Service) close(ctx context.Context, tx *sqlx.Tx, sess store.AgentSession, reason, actor string) error {
	ok, err := store.CloseAgentSession(ctx, tx, sess.ID, reason, s.now())
	if err != nil || !ok {
		return err
	}
	_, err = s.Events.Record(ctx, tx, events.Event{Type: "template.agent_session_closed", Subject: subject(sess.TemplateID), Actor: actor,
		Payload: map[string]any{"reason": reason, "saves": sess.Saves, "session": sess.ID}})
	return err
}

// Revoke ends a session at the admin's request. ErrNotOpen when it already ended.
func (s *Service) Revoke(ctx context.Context, sessionID int64, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		sess, err := store.AgentSessionByID(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if !sess.Open() {
			return ErrNotOpen
		}
		return s.close(ctx, tx, sess, "revoked", actor)
	})
}

// CloseForDraft ends the template's open session because its draft is gone
// (reason published or discarded). It runs in the publishing transaction.
func (s *Service) CloseForDraft(ctx context.Context, tx *sqlx.Tx, templateID int64, reason, actor string) error {
	sess, err := store.OpenAgentSession(ctx, tx, templateID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.close(ctx, tx, sess, reason, actor)
}

// Current returns the template's open session, ErrNotFound when none. A session
// whose time has passed but that the schedule has not closed yet counts as ended.
func (s *Service) Current(ctx context.Context, templateID int64) (store.AgentSession, error) {
	sess, err := store.OpenAgentSession(ctx, s.DB.R, templateID)
	if err != nil {
		return sess, err
	}
	if !sess.ExpiresAt.After(s.Now()) {
		return store.AgentSession{}, store.ErrNotFound
	}
	return sess, nil
}

// ContextOf decodes a session's fixed context.
func ContextOf(sess store.AgentSession) Context { return parseContext(sess.Context) }

// JobType is the job the schedule runs to close expired sessions.
const JobType = "servers.agent_sessions"

// Expire closes every open session whose time has passed, recording
// template.agent_session_closed{expired}. It returns how many it closed.
func (s *Service) Expire(ctx context.Context, actor string) (int, error) {
	list, err := store.ExpiredAgentSessions(ctx, s.DB.R, s.now())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sess := range list {
		err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			cur, err := store.AgentSessionByID(ctx, tx, sess.ID)
			if err != nil || !cur.Open() {
				return err
			}
			n++
			return s.close(ctx, tx, cur, "expired", actor)
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// JobTypes: the expiry job.
func (s *Service) JobTypes() []jobs.Type {
	return []jobs.Type{{
		Name: JobType, Queue: jobs.Maintenance, MaxAttempts: 1, Quiet: true,
		Steps: []jobs.Step{{Name: "expire", Run: func(ctx context.Context, r *jobs.Run) error {
			n, err := s.Expire(ctx, r.Info().Actor())
			if n > 0 {
				r.Log().Info("Closed %d expired agent sessions", n)
			}
			return err
		}}},
	}}
}

// Schedule closes expired sessions every five minutes. Expiry is enforced on
// every request as well; this is what records it.
func (s *Service) Schedule() jobs.Schedule {
	return jobs.Schedule{Name: "servers.agent_sessions", Every: 5 * time.Minute,
		Request: func(context.Context) (jobs.Request, error) {
			return jobs.Request{Type: JobType, CoalescingKey: "agent_sessions"}, nil
		}}
}

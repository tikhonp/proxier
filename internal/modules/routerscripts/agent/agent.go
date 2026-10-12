// Package agent hands a router script's draft to a coding agent
// (docs/processes/router-scripts/script-versions.md, "handing the draft to an
// agent"). Proxier never runs the agent: it gives the admin a prompt with a
// short-lived token that reaches one draft through /agent/v1/router-script
// and nothing else. Secret defaults never leave Proxier (mask.go).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/agentapi"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

var (
	ErrNoDraft   = scripts.ErrNoDraft
	ErrNotFound  = store.ErrNotFound
	ErrNotOpen   = errors.New("the session is not open")
	ErrNoProblem = errors.New("say what the agent should do")
	ErrBadAgent  = errors.New("unknown agent")
)

// Deps is what the service is built from.
type Deps struct {
	DB      *db.DB
	Events  *events.Catalog
	Vault   *vault.Vault
	Scripts *scripts.Service
	// I18n words the check's findings in English for the agent.
	I18n *i18n.Catalog
	Log  *slog.Logger
	// BaseURL is Proxier's own address, as the prompt shows it.
	BaseURL string
	Now     func() time.Time
}

// Service keeps the sessions.
type Service struct{ Deps }

// New returns the service.
func New(d Deps) *Service { return &Service{Deps: d} }

func (s *Service) now() db.Time { return db.At(s.Now()) }

// Open is what the admin chose in the hand-off form.
type Open struct {
	ScriptID    int64
	Problem     string
	Agent       string // claude-code codex other
	For         time.Duration
	IncludeDiff bool
}

// Opened is the answer to opening a session. The token is shown once.
type Opened struct {
	SessionID int64
	Token     string
	Prompt    string
	ExpiresAt time.Time
}

// Context is what the admin chose to show the agent, fixed at opening. The
// check of the draft is always there, computed on each request.
type Context struct {
	Diff bool `json:"diff,omitempty"`
}

// ContextOf decodes a session's fixed context.
func ContextOf(sess store.AgentSession) Context {
	var c Context
	_ = json.Unmarshal([]byte(sess.Context), &c)
	return c
}

// OpenSession hands the script's draft to an agent. It closes the script's
// open session (reason replaced) in the same transaction, and returns the
// token and the prompt that holds it; neither is stored in a readable form.
func (s *Service) OpenSession(ctx context.Context, o Open, actor string) (Opened, error) {
	var ok bool
	if o.Problem, ok = agentapi.Problem(o.Problem); !ok {
		return Opened{}, ErrNoProblem
	}
	if !agentapi.KnownAgent(o.Agent) {
		return Opened{}, ErrBadAgent
	}
	o.For = agentapi.Clamp(o.For)
	sc, err := s.Scripts.Get(ctx, o.ScriptID)
	if err != nil {
		return Opened{}, err
	}
	draft, err := s.Scripts.Draft(ctx, o.ScriptID)
	if err != nil {
		return Opened{}, err
	}
	c := Context{Diff: o.IncludeDiff && draft.BasedOn > 0}
	raw, _ := json.Marshal(c)

	token := agentapi.NewToken()
	opened := s.now()
	expires := db.At(opened.Add(o.For))
	var id int64
	err = s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		// The partial unique index allows one open session per draft: end the
		// old one first. One whose time already passed ended by expiry, not by us.
		if old, err := store.OpenAgentSession(ctx, tx, o.ScriptID); err == nil {
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
			ScriptID: o.ScriptID, TokenLookup: s.Vault.Lookup(token), Problem: o.Problem, Context: string(raw),
			Agent: o.Agent, OpenedAt: opened, ExpiresAt: expires,
		})
		if err != nil {
			return err
		}
		_, err = s.Events.Record(ctx, tx, events.Event{Time: opened, Type: "routerscript.agent_session_opened", Subject: scripts.Subject(o.ScriptID), Actor: actor,
			Payload: map[string]any{"expires_at": expires.Format(db.TimeLayout), "agent": o.Agent, "context": map[string]any{"diff": c.Diff}, "session": id}})
		return err
	})
	if err != nil {
		return Opened{}, err
	}
	return Opened{
		SessionID: id, Token: token, ExpiresAt: expires.Time,
		Prompt: Prompt(PromptData{Agent: o.Agent, Script: sc.Name, Problem: o.Problem, BaseURL: s.BaseURL, Token: token, Expires: expires.Time, Context: c, BasedOn: draft.BasedOn}),
	}, nil
}

// close ends an open session in tx and records routerscript.agent_session_closed.
func (s *Service) close(ctx context.Context, tx *sqlx.Tx, sess store.AgentSession, reason, actor string) error {
	ok, err := store.CloseAgentSession(ctx, tx, sess.ID, reason, s.now())
	if err != nil || !ok {
		return err
	}
	_, err = s.Events.Record(ctx, tx, events.Event{Time: s.now(), Type: "routerscript.agent_session_closed", Subject: scripts.Subject(sess.ScriptID), Actor: actor,
		Payload: map[string]any{"reason": reason, "saves": sess.Saves, "session": sess.ID}})
	return err
}

// Revoke ends a session of the script at the admin's request: ErrNotFound
// when the script has no such session, ErrNotOpen when it already ended.
func (s *Service) Revoke(ctx context.Context, scriptID, sessionID int64, actor string) error {
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		sess, err := store.AgentSessionByID(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if sess.ScriptID != scriptID {
			return ErrNotFound
		}
		if !sess.Open() {
			return ErrNotOpen
		}
		return s.close(ctx, tx, sess, "revoked", actor)
	})
}

// CloseForDraft ends the script's open session because its draft is gone
// (reason published or discarded). It runs in that transaction.
func (s *Service) CloseForDraft(ctx context.Context, tx *sqlx.Tx, scriptID int64, reason, actor string) error {
	sess, err := store.OpenAgentSession(ctx, tx, scriptID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.close(ctx, tx, sess, reason, actor)
}

// Current returns the script's open session, ErrNotFound when none. A session
// whose time has passed but that the schedule has not closed yet counts as ended.
func (s *Service) Current(ctx context.Context, scriptID int64) (store.AgentSession, error) {
	sess, err := store.OpenAgentSession(ctx, s.DB.R, scriptID)
	if err != nil {
		return sess, err
	}
	if !sess.ExpiresAt.After(s.Now()) {
		return store.AgentSession{}, store.ErrNotFound
	}
	return sess, nil
}

// JobType is the job the schedule runs to close expired sessions.
const JobType = "routerscripts.agent_sessions"

// Expire closes every open session whose time has passed, recording
// routerscript.agent_session_closed{expired}. It returns how many it closed.
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
	return jobs.Schedule{Name: JobType, Every: 5 * time.Minute,
		Request: func(context.Context) (jobs.Request, error) {
			return jobs.Request{Type: JobType, CoalescingKey: "agent_sessions"}, nil
		}}
}

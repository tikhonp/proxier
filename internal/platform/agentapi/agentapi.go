// Package agentapi is what every agent hand-off shares: servers hands a
// template draft to a coding agent, routerscripts a script draft. Proxier never
// runs the agent. A module gives the admin a prompt with a short-lived bearer
// token that reaches one draft through the module's part of /agent/v1. Each
// module keeps its own sessions; this package holds the token, the limits of a
// session, the answers every API gives, and the one-time hand-off form.
package agentapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/db"
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
	MaxRequests     = 600
	MaxPerMinute    = 60
	MaxProblemRunes = 4000
)

// Agents the prompt is worded for.
var Agents = []string{"claude-code", "codex", "other"}

// FormDurations are the access lengths the hand-off form offers, in minutes.
var FormDurations = []string{"30", "120", "480"}

// NewToken is a new agent token. Only vault.Lookup of it is stored.
func NewToken() string { return TokenPrefix + vault.NewToken() }

// Problem trims what the admin asked the agent to do; ok is false when it is
// empty or longer than MaxProblemRunes.
func Problem(text string) (string, bool) {
	text = strings.TrimSpace(text)
	return text, text != "" && utf8.RuneCountInString(text) <= MaxProblemRunes
}

// KnownAgent reports whether the prompt has a wording for agent.
func KnownAgent(agent string) bool { return slices.Contains(Agents, agent) }

// Clamp is the access length a session gets for d: the default for none, at
// most MaxDuration.
func Clamp(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultDuration
	case d > MaxDuration:
		return MaxDuration
	}
	return d
}

// FormDuration reads the form's minutes; anything it doesn't offer is the default.
func FormDuration(minutes string) time.Duration {
	if !slices.Contains(FormDurations, minutes) {
		return DefaultDuration
	}
	n, _ := strconv.Atoi(minutes)
	return time.Duration(n) * time.Minute
}

// Mask hides the token in a prompt for display.
func Mask(prompt, token string) string {
	return strings.ReplaceAll(prompt, token, TokenPrefix+"••••••••••••")
}

// Bearer is the agent token of the request's Authorization header, "" when
// there is none or it isn't one.
func Bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	tok := strings.TrimSpace(h[7:])
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) > 128 {
		return ""
	}
	return tok
}

// Rate is a session's request counters: all requests, and those of the
// current fixed minute.
type Rate struct {
	Requests    int
	MinuteStart db.Time
	MinuteCount int
}

// Count lets one more request of a session that ends at expires in. When it
// may not, ok is false and retry is how long to wait.
func (r Rate) Count(now, expires db.Time) (next Rate, retry time.Duration, ok bool) {
	if r.Requests >= MaxRequests {
		return r, expires.Sub(now.Time), false
	}
	start, count := r.MinuteStart, r.MinuteCount
	if start.IsZero() || !now.Before(start.Add(time.Minute)) {
		start, count = now, 0
	}
	if count >= MaxPerMinute {
		return r, start.Add(time.Minute).Sub(now.Time), false
	}
	return Rate{Requests: r.Requests + 1, MinuteStart: start, MinuteCount: count + 1}, 0, true
}

// Fail answers {"error": msg}.
func Fail(c *echo.Context, code int, msg string) error {
	return c.JSON(code, map[string]string{"error": msg})
}

// SessionEnded is the message of every refused token.
const SessionEnded = "session ended"

// Ended answers a token that doesn't open a session. An ended, expired and
// unknown token get the same answer, so they can't be told apart.
func Ended(c *echo.Context) error {
	c.Response().Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	return Fail(c, http.StatusUnauthorized, SessionEnded)
}

// TooMany answers a request over the session's limits.
func TooMany(c *echo.Context, retry time.Duration) error {
	c.Response().Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
	return Fail(c, http.StatusTooManyRequests, "too many requests")
}

// Matches reports whether a conditional header value names etag.
func Matches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(p), "W/")
		if p == etag {
			return true
		}
	}
	return false
}

// Forms remembers the hand-off forms that already opened a session. The
// prompt is shown once, in the answer to the post; reloading that answer
// posts the form again, which must not hand the draft over a second time.
type Forms struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// Take reports whether id is new, and remembers it for an hour.
func (u *Forms) Take(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.seen == nil {
		u.seen = map[string]time.Time{}
	}
	for k, at := range u.seen {
		if time.Since(at) > time.Hour {
			delete(u.seen, k)
		}
	}
	if _, dup := u.seen[id]; dup {
		return false
	}
	u.seen[id] = time.Now()
	return true
}

// NewFormID names one rendering of a hand-off form.
func NewFormID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

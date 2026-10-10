// Package health decides whether each active server works from Russia
// (docs/processes/servers/server-health.md): the checks and their jobs, the
// verdict, flap protection, freezing while home has no internet, pauses and
// reminders. Evaluate is the verdict as a pure function; everything else
// stores results and applies what it says.
package health

import (
	"time"
)

// Health states.
const (
	Healthy  = "healthy"
	Degraded = "degraded"
	Blocked  = "blocked"
	Down     = "down"
	Unknown  = "unknown"
	Paused   = "paused"
)

// Classes of a self-check result.
const (
	SelfOK          = "ok"
	SelfStackFailed = "stack-failed"
	SelfUnreachable = "unreachable"
	SelfHostKey     = "host-key-changed"
)

// Home states (the reference check).
const (
	HomeOnline             = "online"
	HomeForeignUnreachable = "foreign-unreachable"
	HomeOffline            = "offline"
)

// WaitForExternal is how long an evaluation that needs external data waits
// for the on-demand check it started.
const WaitForExternal = 2 * time.Minute

// Thresholds are the settings the verdict uses.
type Thresholds struct {
	// A proxy test is slow when its first byte took longer than SlowFirstByte
	// or the download ran below SlowKbps.
	SlowFirstByte time.Duration
	SlowKbps      int
	// Confirmations is how many consecutive counted evaluations must agree
	// before the state changes.
	Confirmations int
}

// DefaultThresholds are the settings' defaults.
var DefaultThresholds = Thresholds{SlowFirstByte: 2 * time.Second, SlowKbps: 1000, Confirmations: 2}

// Failure is one failed check of the self-check.
type Failure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// SelfResult is a fresh, conclusive self-check.
type SelfResult struct {
	At       time.Time
	Class    string // SelfOK … SelfHostKey
	Failures []Failure
	Error    string // why it was unreachable
}

// ProxyResult is a fresh, conclusive proxy test of one endpoint.
type ProxyResult struct {
	At          time.Time
	OK          bool
	Class       string // tcp-timeout stalled …
	Error       string
	FirstByteMS int
	Kbps        int
}

// NodeAnswer is one check-host node's answer.
type NodeAnswer struct {
	Node, Country string
	Abroad        bool
	OK            bool
	MS            int
	Error         string
}

// ExternalResult is a fresh external check that really ran.
type ExternalResult struct {
	At    time.Time
	Nodes []NodeAnswer
}

// CheckState is everything the verdict looks at.
type CheckState struct {
	Self      *SelfResult            // nil: missing or stale
	Proxy     map[string]ProxyResult // by endpoint key; a missing key is missing
	Endpoints []string               // the server's endpoint keys, in order
	External  *ExternalResult        // nil: no external data
	Home      string                 // online foreign-unreachable offline
	Paused    bool
	// PausedUntil is zero for "until resumed".
	PausedUntil time.Time
	// AwaitingExternal is when an on-demand external check was asked for; zero
	// when none is awaited.
	AwaitingExternal time.Time
}

// Reason is a message key with arguments; pages and notifications render it
// in the admin's language.
type Reason struct {
	Key  string         `json:"key"`
	Args map[string]any `json:"args,omitempty"`
}

// Outcome is a verdict.
type Outcome struct {
	// Frozen: home has no internet, so nothing is decided.
	Frozen bool
	// Wait: the candidate needs the external check that is on its way.
	Wait bool
	// Candidate is one of the states above.
	Candidate string
	// Immediate applies without confirmation: paused, host key.
	Immediate bool
	// Rule names the rule that matched: "1", "2", "2a", "3", "4", "5", "5a", "6", …
	Rule   string
	Reason Reason
	// ProxyRound is the newest fresh proxy result's time: the round this
	// evaluation saw. A counted evaluation is the first to see a new round.
	ProxyRound time.Time
	Summary    map[string]any
}

// ext counts nodes of an external result.
type ext struct {
	abroadOK, abroad, ruOK, ru int
	abroadCountries            []string
}

func countNodes(e *ExternalResult) ext {
	var x ext
	seen := map[string]bool{}
	if e == nil {
		return x
	}
	for _, n := range e.Nodes {
		if n.Abroad {
			x.abroad++
			if n.OK {
				x.abroadOK++
				if n.Country != "" && !seen[n.Country] {
					seen[n.Country] = true
					x.abroadCountries = append(x.abroadCountries, n.Country)
				}
			}
		} else {
			x.ru++
			if n.OK {
				x.ruOK++
			}
		}
	}
	return x
}

// Evaluate gives the verdict for a server, applying the rules of
// docs/processes/servers/server-health.md#verdict from top to bottom; the first match wins.
// Every input matches exactly one rule.
func Evaluate(in CheckState, th Thresholds, now time.Time) Outcome {
	o := evaluate(in, th, now)
	o.Summary["rule"] = o.Rule
	return o
}

func evaluate(in CheckState, th Thresholds, now time.Time) Outcome {
	if th.Confirmations == 0 {
		th = DefaultThresholds
	}
	o := Outcome{Summary: map[string]any{}}

	// 1: paused, at once.
	if in.Paused {
		o.Rule, o.Candidate, o.Immediate = "1", Paused, true
		args := map[string]any{}
		if !in.PausedUntil.IsZero() {
			args["until"] = in.PausedUntil.UTC().Format(time.RFC3339)
		}
		o.Reason = Reason{Key: "health.reason.paused", Args: args}
		if in.PausedUntil.IsZero() {
			o.Reason.Key = "health.reason.paused_forever"
		}
		return o
	}
	// 2: home has no internet: nothing is decided.
	if in.Home != "" && in.Home != HomeOnline {
		o.Rule, o.Frozen = "2", true
		o.Reason = Reason{Key: "health.reason.frozen_" + map[string]string{HomeOffline: "offline", HomeForeignUnreachable: "foreign"}[in.Home]}
		return o
	}
	// 2a: the host key changed: unknown, at once.
	if in.Self != nil && in.Self.Class == SelfHostKey {
		o.Rule, o.Candidate, o.Immediate = "2a", Unknown, true
		o.Reason = Reason{Key: "health.reason.host_key"}
		o.Summary["self"] = SelfHostKey
		return o
	}

	// What the proxy tests say.
	var passed, failed, slow int
	var failing []string
	var firstSlow *ProxyResult
	missing := len(in.Endpoints) == 0
	proxySummary := map[string]any{}
	for _, k := range in.Endpoints {
		r, ok := in.Proxy[k]
		if !ok {
			missing = true
			continue
		}
		if r.At.After(o.ProxyRound) {
			o.ProxyRound = r.At
		}
		entry := map[string]any{"ok": r.OK}
		if r.OK {
			passed++
			entry["first_byte_ms"], entry["kbps"] = r.FirstByteMS, r.Kbps
			if time.Duration(r.FirstByteMS)*time.Millisecond > th.SlowFirstByte || r.Kbps < th.SlowKbps {
				slow++
				if firstSlow == nil {
					rr := r
					firstSlow = &rr
				}
			}
		} else {
			failed++
			failing = append(failing, k)
			entry["class"], entry["error"] = r.Class, r.Error
		}
		proxySummary[k] = entry
	}
	o.Summary["proxy"] = proxySummary
	if in.Self != nil {
		o.Summary["self"] = in.Self.Class
	} else {
		o.Summary["self"] = "missing"
	}
	x := countNodes(in.External)
	// An external result without a node abroad cannot confirm anything.
	external := in.External
	if x.abroad == 0 {
		external = nil
	}
	if external != nil {
		o.Summary["external"] = map[string]any{"abroad_ok": x.abroadOK, "abroad": x.abroad, "ru_ok": x.ruOK, "ru": x.ru}
	}

	// 3: some endpoint has no result yet.
	if missing {
		o.Rule, o.Candidate = "3", Unknown
		o.Reason = Reason{Key: "health.reason.waiting"}
		o.ProxyRound = time.Time{}
		return o
	}

	self := ""
	if in.Self != nil {
		self = in.Self.Class
	}
	n := len(in.Endpoints)

	// 4, 5, 5a: at least one endpoint passes.
	if passed > 0 {
		switch {
		case passed == n && slow == 0 && (self == SelfOK || self == ""):
			o.Rule, o.Candidate = "4", Healthy
		case slow > 0 || passed < n || self == SelfStackFailed:
			o.Rule, o.Candidate = "5", Degraded
			o.Reason = degradedReason(slow, firstSlow, failing, in.Proxy, in.Self, th)
		default: // every endpoint passes and SSH is unreachable
			o.Rule, o.Candidate = "5a", Degraded
			o.Reason = Reason{Key: "health.reason.ssh_unreachable", Args: map[string]any{"error": selfError(in.Self)}}
		}
		return o
	}

	// Every endpoint fails.
	failure := proxyFailure(in.Proxy[in.Endpoints[0]])
	reason := func(key string, extra map[string]any) Reason {
		args := map[string]any{"failure": failure, "ru_ok": x.ruOK, "ru": x.ru, "abroad_ok": x.abroadOK, "abroad": x.abroad}
		for k, v := range extra {
			args[k] = v
		}
		return Reason{Key: key, Args: args}
	}
	var rule string
	switch {
	case self == SelfStackFailed: // 6
		rule, o.Candidate = "6", Down
		o.Reason = Reason{Key: "health.reason.stack_broken", Args: map[string]any{"what": stackProblem(in.Self)}}
	case external != nil && x.abroadOK >= 1: // 7
		rule, o.Candidate = "7", Blocked
		o.Reason = reason("health.reason.blocked_abroad", map[string]any{"countries": x.abroadCountries})
	case self == SelfOK && external == nil: // 8
		rule, o.Candidate = "8", Blocked
		o.Reason = reason("health.reason.blocked_unconfirmed", nil)
	case self == SelfOK: // 8a: external says nothing connects abroad
		rule, o.Candidate = "8a", Down
		o.Reason = reason("health.reason.down_stack_runs", nil)
	case self == SelfUnreachable && external != nil: // 9
		rule, o.Candidate = "9", Down
		o.Reason = reason("health.reason.down_unreachable", nil)
	case self == SelfUnreachable: // 10
		rule, o.Candidate = "10", Down
		o.Reason = reason("health.reason.down_unreachable_unconfirmed", nil)
	default: // 11: self-check missing, and no node abroad connects
		rule, o.Candidate = "11", Unknown
		key := "health.reason.not_enough_none"
		if external != nil {
			key = "health.reason.not_enough_zero"
		}
		o.Reason = reason(key, nil)
	}
	o.Rule = rule

	// Rules that lean on external data wait for the check that is on its way.
	switch rule {
	case "7", "8", "8a", "9", "10", "11":
		if !in.AwaitingExternal.IsZero() && now.Sub(in.AwaitingExternal) < WaitForExternal &&
			(in.External == nil || in.External.At.Before(in.AwaitingExternal)) {
			o.Wait = true
		}
	}
	return o
}

func selfError(s *SelfResult) string {
	if s == nil {
		return ""
	}
	return s.Error
}

// proxyFailure says why a proxy test failed, in the words of the check.
func proxyFailure(r ProxyResult) string {
	switch {
	case r.Class == "" && r.Error == "":
		return "failed"
	case r.Error == "":
		return r.Class
	case r.Class == "":
		return r.Error
	}
	return r.Class + ": " + r.Error
}

func degradedReason(slow int, firstSlow *ProxyResult, failing []string, proxy map[string]ProxyResult, self *SelfResult, th Thresholds) Reason {
	switch {
	case slow > 0 && firstSlow != nil:
		if time.Duration(firstSlow.FirstByteMS)*time.Millisecond > th.SlowFirstByte {
			return Reason{Key: "health.reason.slow_first_byte", Args: map[string]any{"seconds": float64(firstSlow.FirstByteMS) / 1000}}
		}
		return Reason{Key: "health.reason.slow_throughput", Args: map[string]any{"kbps": firstSlow.Kbps}}
	case len(failing) > 0:
		return Reason{Key: "health.reason.endpoint_fails", Args: map[string]any{"endpoint": failing[0], "failure": proxyFailure(proxy[failing[0]])}}
	}
	return Reason{Key: "health.reason.stack_part", Args: map[string]any{"what": stackProblem(self)}}
}

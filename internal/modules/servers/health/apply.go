package health

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// Detail is the health_detail column: the latest evaluation, kept for the UI
// even when it changed nothing.
type Detail struct {
	At        time.Time      `json:"at"`
	Candidate string         `json:"candidate"`
	Rule      string         `json:"rule"`
	Reason    Reason         `json:"reason"`
	Summary   map[string]any `json:"summary,omitempty"`
	// Awaiting is when an on-demand external check was asked for; it is kept
	// between evaluations and cleared by the check that answers it.
	Awaiting string `json:"awaiting_external,omitempty"`
}

// ParseDetail reads a stored detail.
func ParseDetail(raw string) Detail {
	var d Detail
	_ = json.Unmarshal([]byte(raw), &d)
	return d
}

func (d Detail) String() string {
	b, _ := json.Marshal(d)
	return string(b)
}

// ParseReason reads a stored reason ("" is no reason).
func ParseReason(raw string) (Reason, bool) {
	var r Reason
	if raw == "" || json.Unmarshal([]byte(raw), &r) != nil || r.Key == "" {
		return Reason{}, false
	}
	return r, true
}

func (r Reason) String() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// RenderReason writes a reason in the localizer's language. Arguments named
// until are times; countries is a list of country codes; seconds is a number
// of seconds shown with one decimal.
func RenderReason(loc *i18n.Localizer, r Reason) string {
	if r.Key == "" {
		return ""
	}
	args := i18n.Args{}
	for k, v := range r.Args {
		args[k] = v
	}
	if u, ok := r.Args["until"].(string); ok {
		if t, err := time.Parse(time.RFC3339, u); err == nil {
			args["until"] = loc.Time(t)
		}
	}
	if cs, ok := r.Args["countries"].([]string); ok {
		args["countries"] = joinCountries(loc, cs)
	} else if cs, ok := r.Args["countries"].([]any); ok {
		var codes []string
		for _, c := range cs {
			if s, ok := c.(string); ok {
				codes = append(codes, s)
			}
		}
		args["countries"] = joinCountries(loc, codes)
	}
	if sec, ok := r.Args["seconds"].(float64); ok {
		args["seconds"] = strconv.FormatFloat(sec, 'f', 1, 64)
	}
	if !loc.Has(r.Key) {
		return r.Key
	}
	return loc.T(r.Key, args)
}

func joinCountries(loc *i18n.Localizer, codes []string) string {
	names := make([]string, 0, len(codes))
	for _, c := range codes {
		names = append(names, country.Name(strings.ToUpper(c), string(loc.Lang)))
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + loc.T("health.and") + names[len(names)-1]
}

// english is the localizer for texts stored in events (the Activity page and
// the notifier's fallback show them as they are).
func (s *Service) english() *i18n.Localizer { return s.I18n.Localizer(i18n.EN, time.UTC) }

// Apply writes a verdict in tx. A frozen or waiting verdict changes nothing. An
// immediate one (paused, host key) applies at once. Any other applies after
// flap protection, and only a counted evaluation, the first to see a new proxy
// round, moves the candidate (docs/processes/servers/server-health.md,
// "Flap protection"). It
// reports whether the state changed.
func (s *Service) Apply(ctx context.Context, tx *sqlx.Tx, id int64, o Outcome, actor string) (bool, error) {
	if o.Frozen || o.Wait {
		return false, nil
	}
	h, err := store.GetHealth(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if h.State != "active" || h.Retiring {
		return false, nil
	}
	paused := !h.PausedUntil.IsZero()
	// A pause decided while this verdict was being made wins, and so does a
	// resume: a stale "paused" must not undo it.
	if paused != (o.Candidate == Paused) {
		return false, nil
	}
	now := db.At(s.now())
	detail := Detail{At: now.Time, Candidate: o.Candidate, Rule: o.Rule, Reason: o.Reason, Summary: o.Summary, Awaiting: ParseDetail(h.Detail).Awaiting}

	change := func(count int, proxyAt db.Time) (bool, error) {
		if err := s.recordChange(ctx, tx, id, h.Health, o, now, actor); err != nil {
			return false, err
		}
		if err := store.SetEvaluation(ctx, tx, id, o.Candidate, count, proxyAt, detail.String()); err != nil {
			return false, err
		}
		return true, nil
	}

	if o.Immediate {
		if h.Health != o.Candidate {
			return change(0, db.Time{})
		}
		return false, store.SetDetail(ctx, tx, id, detail.String())
	}

	counted := !o.ProxyRound.IsZero() && o.ProxyRound.After(h.CountedProxyAt.Time)
	if !counted {
		return false, store.SetDetail(ctx, tx, id, detail.String())
	}
	proxyAt := db.At(o.ProxyRound)
	cfg, err := s.Config(ctx)
	if err != nil {
		return false, err
	}
	switch {
	case h.Health == Unknown && o.Candidate != Unknown:
		return change(0, proxyAt) // the first state after unknown
	case o.Candidate == h.Health:
		return false, store.SetEvaluation(ctx, tx, id, o.Candidate, 0, proxyAt, detail.String())
	}
	count := 1
	if h.Candidate.Valid && h.Candidate.String == o.Candidate {
		count = h.CandidateCount + 1
	}
	if count >= cfg.Thresholds.Confirmations {
		return change(0, proxyAt)
	}
	return false, store.SetEvaluation(ctx, tx, id, o.Candidate, count, proxyAt, detail.String())
}

// recordChange moves the server to the candidate and records the event.
func (s *Service) recordChange(ctx context.Context, tx *sqlx.Tx, id int64, from string, o Outcome, now db.Time, actor string) error {
	if err := store.SetHealthState(ctx, tx, id, o.Candidate, now, o.Reason.String()); err != nil {
		return err
	}
	_, err := s.Events.Record(ctx, tx, events.Event{
		Type: "server.health_changed", Subject: store.ServerSubject(id), Actor: actor,
		Payload: map[string]any{
			"from": from, "to": o.Candidate, "reason": RenderReason(s.english(), o.Reason),
			"reason_key": o.Reason.Key, "reason_args": o.Reason.Args, "summary": o.Summary,
		},
	})
	return err
}

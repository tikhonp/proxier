package routers

import (
	"strings"

	"github.com/tikhonp/proxier/internal/platform/tailnet"
)

// Hop results.
const (
	HopUp         = "up"
	HopOff        = "off"
	HopOffline    = "offline"
	HopRefused    = "refused"
	HopKeyChanged = "key-changed"
	HopUnknown    = "unknown" // first contact: the key isn't confirmed yet
	HopNotTried   = "not-tried"
)

// Hop is a row of a connect failure's per-hop table. The page words it:
// Kind (tailnet, jump, router) and Label name the hop ("tailnet node
// proxier", "jump host parents-pi", "router 10.230.2.1").
type Hop struct {
	Kind   string
	Label  string
	Result string // up, off, offline, refused, key-changed, unknown, not-tried
	Detail string // the failing hop's problem
	Via    string // not tried: the hop it is reached only through
}

// Hops is pure: the per-hop table of a sync that failed at connect, from
// the row's failing hop and problem and the tailnet node's state now. The
// tailnet row shows only when the first hop uses the tailnet, the jump row
// only with a jump host; hops before the failing one are up, the ones after
// it not tried.
func Hops(r Router, s Sync, node tailnet.Status) []Hop {
	var out []Hop
	if r.Conn.Tailnet {
		name := node.Name
		if name == "" {
			name = "proxier"
		}
		out = append(out, Hop{Kind: "tailnet", Label: name, Result: HopUp})
		if node.State != tailnet.Running {
			out[0].Result = HopOff
		}
	}
	if r.Conn.JumpHost != "" {
		out = append(out, Hop{Kind: "jump", Label: r.Conn.JumpHost, Result: HopUp})
	}
	out = append(out, Hop{Kind: "router", Label: r.Conn.Host, Result: HopUp})
	failed := -1
	for i := range out {
		if out[i].Kind == s.Hop || (s.Hop == "" && out[i].Kind == "router") {
			failed = i
			break
		}
	}
	if failed < 0 { // a tailnet failure of a router not using it: the router row
		failed = len(out) - 1
	}
	out[failed].Result, out[failed].Detail = hopResult(s), s.Error
	for i := failed + 1; i < len(out); i++ {
		out[i].Result = HopNotTried
		if out[failed].Kind == "jump" {
			out[i].Via = out[failed].Label
		}
	}
	return out
}

// hopResult reads the failing hop's result from its problem sentence
// (ProblemOf's words).
func hopResult(s Sync) string {
	switch {
	case s.Hop == "tailnet":
		return HopOff
	case strings.Contains(s.Error, "refused Proxier's key"):
		return HopRefused
	case strings.Contains(s.Error, "host key changed"):
		return HopKeyChanged
	case strings.HasPrefix(s.Error, "First contact"):
		return HopUnknown
	}
	return HopOffline
}

package output

import (
	"slices"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
)

// Masked stands in for a credential or a secret path in a masked preview.
const Masked = endpoint.Masked

// Server is a member the catalog serves now.
type Server struct {
	ID          int64
	Name        string
	Health      string
	HealthSince time.Time
	Endpoints   []endpoint.Endpoint
}

// Hide is a subscription's "hide unhealthy servers".
type Hide struct {
	On     bool
	States []string
	Grace  time.Duration
}

// HideStates are the states a subscription may hide; paused never hides.
var HideStates = []string{"blocked", "down", "degraded", "unknown"}

// Hidden is a server left out, and for how long it has been in its state.
type Hidden struct {
	Server Server
	For    time.Duration
}

// Select keeps the order. allHidden: hiding would have left nothing, so served
// is every server and hidden is empty.
func Select(servers []Server, h Hide, now time.Time) (served []Server, hidden []Hidden, allHidden bool) {
	for _, s := range servers {
		if h.On && s.Health != "paused" && slices.Contains(h.States, s.Health) {
			if d := now.Sub(s.HealthSince); d >= h.Grace {
				hidden = append(hidden, Hidden{Server: s, For: d})
				continue
			}
		}
		served = append(served, s)
	}
	if len(served) == 0 && len(hidden) > 0 {
		return servers, nil, true
	}
	return served, hidden, false
}

// URIs builds every endpoint of every server, in order; masked hides their
// secrets.
func URIs(servers []Server, masked bool) ([]string, error) {
	var out []string
	for _, s := range servers {
		for _, e := range s.Endpoints {
			uri, err := endpoint.URI(e)
			if masked {
				uri, err = endpoint.MaskedURI(e)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, uri)
		}
	}
	return out, nil
}

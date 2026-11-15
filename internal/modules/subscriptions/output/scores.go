package output

import (
	"math"
	"slices"
	"strconv"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
)

var ScoreModes = []string{"", "ip", "claude", "chatgpt"}

// WithScores copies endpoints before changing names, so previews cannot alter
// the catalog or another subscription's output. The catalog supplies only
// current, fresh scores and omits all scores when the integration is off.
func WithScores(servers []Server, mode string) []Server {
	label := map[string]string{"ip": "IP", "claude": "Claude", "chatgpt": "ChatGPT"}[mode]
	if label == "" {
		return servers
	}
	out := slices.Clone(servers)
	for i := range out {
		out[i].Endpoints = append([]endpoint.Endpoint(nil), out[i].Endpoints...)
		for j := range out[i].Endpoints {
			e := &out[i].Endpoints[j]
			score, ok := out[i].Scores[e.Key][mode]
			if ok && !math.IsNaN(score) && !math.IsInf(score, 0) && score >= 0 && score <= 100 {
				e.DisplayName += " · " + label + " " + strconv.FormatFloat(score, 'f', -1, 64)
			}
		}
	}
	return out
}

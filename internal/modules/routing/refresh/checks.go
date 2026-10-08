package refresh

import "github.com/tikhonp/proxier/internal/modules/routing/snapshot"

// The reasons a snapshot is held back.
const (
	ReasonEmpty  = "empty"
	ReasonShrink = "shrink"
)

// Check is the safety check, pure: "" when the new set may be accepted.
// Names are compared by name, so a name changing form isn't lost. A set of
// fewer than minNames names is never checked for shrink; lostPct is rounded
// down (100 for an empty set).
func Check(old, new snapshot.Set, minNames, maxLostPct int) (reason string, lostPct int) {
	if new.Count() == 0 {
		return ReasonEmpty, 100
	}
	oldNames := names(old)
	if len(oldNames) == 0 {
		return "", 0
	}
	newNames := names(new)
	lost := 0
	for n := range oldNames {
		if !newNames[n] {
			lost++
		}
	}
	pct := lost * 100 / len(oldNames)
	if len(oldNames) >= minNames && lost*100 > maxLostPct*len(oldNames) {
		return ReasonShrink, pct
	}
	return "", pct
}

func names(s snapshot.Set) map[string]bool {
	out := make(map[string]bool, s.Count())
	for _, n := range s.Suffix {
		out[n] = true
	}
	for _, n := range s.Exact {
		out[n] = true
	}
	return out
}

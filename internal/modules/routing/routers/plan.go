package routers

import (
	"sort"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
)

// Want is a tag a router's list wants, with what it installs.
type Want struct {
	Tag       string
	ServiceID int64
	Selector  string // or "custom"
	Position  int
	Entries   []routeros.Entry
}

// Applied is what Proxier last installed under a tag.
type Applied struct {
	Hash          string
	Suffix, Exact int
	At            time.Time
}

// Extra is what the admin asked for besides the list (3f).
type Extra struct {
	Remove    []string // unmanaged tags to remove
	RemoveAll bool     // removing the router: every applied tag goes, nothing is desired
}

// Plan actions and reasons.
const (
	Unchanged = "unchanged"
	Record    = "record"
	Update    = "update"
	Remove    = "remove"
	Unmanaged = "unmanaged"
	Forget    = "forget"

	WhyNew            = "new"
	WhyChanged        = "changed"
	WhyDrift          = "drift"
	WhyLeftList       = "left-list"
	WhyOwnsNothing    = "owns-nothing"
	WhyNeverInstalled = "never-installed"
	WhyAsked          = "asked"
)

// TagPlan is what a sync does with one tag.
type TagPlan struct {
	Tag     string           `json:"tag"`
	Service string           `json:"service"`
	Want    int              `json:"want"` // -1: not desired
	Have    int              `json:"have"` // DNS entries on the router
	Action  string           `json:"action"`
	Why     string           `json:"why,omitempty"`
	Gains   bool             `json:"gains,omitempty"`
	Entries []routeros.Entry `json:"-"`
	pos     int
}

// Pushes reports whether the row sends anything to the router.
func (p TagPlan) Pushes() bool { return p.Action == Update || p.Action == Remove }

// Desired is what a router following the list gets: every member's tag with
// the names it owns (a tag owning nothing is desired with none).
func Desired(v lists.View) []Want {
	out := make([]Want, 0, len(v.Members))
	for i, m := range v.Members {
		o := v.Result.Services[i]
		w := Want{Tag: m.Service.Tag, ServiceID: m.Service.ID, Selector: m.Service.Selector, Position: m.Position}
		if m.Service.Source == selector.Custom {
			w.Selector = "custom"
		}
		for _, n := range o.Suffix {
			w.Entries = append(w.Entries, routeros.Entry{Name: n})
		}
		for _, n := range o.Exact {
			w.Entries = append(w.Entries, routeros.Entry{Name: n, Exact: true})
		}
		out = append(out, w)
	}
	return out
}

// MakePlan decides per tag of want ∪ router ∪ applied (router-sync.md step
// 4). Rows come in push order first: updates that gain names (list order),
// other updates (list order), removals (by tag); then the rest, desired tags
// in list order before the others by tag.
func MakePlan(want []Want, st State, applied map[string]Applied, x Extra) []TagPlan {
	asked := map[string]bool{}
	for _, t := range x.Remove {
		asked[t] = true
	}
	desired := map[string]bool{}
	var out []TagPlan
	if !x.RemoveAll {
		for i, w := range want {
			desired[w.Tag] = true
			t := st.Tags[w.Tag]
			app, was := applied[w.Tag]
			p := TagPlan{Tag: w.Tag, Service: w.Selector, Want: len(w.Entries), Have: t.Entries(), Entries: w.Entries, pos: i}
			switch {
			case len(w.Entries) == 0:
				switch {
				case !t.empty():
					p.Action, p.Why = Remove, WhyOwnsNothing
				case was:
					p.Action = Forget
				default:
					p.Action = Unchanged
				}
			case Matches(t, w.Entries, st.Names):
				p.Action = Record
				if was && app.Hash == routeros.EntriesHash(w.Entries) {
					p.Action = Unchanged
				}
			default:
				p.Action = Update
				switch {
				case t.empty() && !was:
					p.Why = WhyNew
				case was && app.Hash == routeros.EntriesHash(w.Entries):
					p.Why = WhyDrift
				default:
					p.Why = WhyChanged
				}
				have := map[string]bool{}
				if t != nil {
					for _, d := range t.DNS {
						have[d.Name] = true
					}
				}
				for _, e := range w.Entries {
					if !have[e.Name] {
						p.Gains = true
						break
					}
				}
			}
			out = append(out, p)
		}
	}
	others := map[string]bool{}
	for tag := range st.Tags {
		others[tag] = true
	}
	for tag := range applied {
		others[tag] = true
	}
	for tag := range others {
		if desired[tag] {
			continue
		}
		t := st.Tags[tag]
		_, was := applied[tag]
		p := TagPlan{Tag: tag, Want: -1, Have: t.Entries(), pos: len(want)}
		switch {
		case !t.empty() && (was || asked[tag]):
			p.Action, p.Why = Remove, WhyLeftList
			if !was {
				p.Why = WhyAsked
			}
		case !t.empty():
			p.Action, p.Why = Unmanaged, WhyNeverInstalled
		case was:
			p.Action = Forget
		default:
			continue
		}
		out = append(out, p)
	}
	rank := func(p TagPlan) int {
		switch {
		case p.Action == Update && p.Gains:
			return 0
		case p.Action == Update:
			return 1
		case p.Action == Remove:
			return 2
		}
		return 3
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if rank(a) == 2 {
			return a.Tag < b.Tag
		}
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		return a.Tag < b.Tag
	})
	return out
}

// Blocks are the plan's pushes in order.
func Blocks(plan []TagPlan) []routeros.Block {
	var out []routeros.Block
	for _, p := range plan {
		switch p.Action {
		case Update:
			out = append(out, routeros.Block{Tag: p.Tag, Entries: p.Entries})
		case Remove:
			out = append(out, routeros.Block{Tag: p.Tag, Remove: true})
		}
	}
	return out
}

// Count tallies a plan as a sync row stores it.
func Count(plan []TagPlan) (added, updated, removed, unchanged, recorded int) {
	for _, p := range plan {
		switch p.Action {
		case Update:
			if p.Why == WhyNew {
				added++
			} else {
				updated++
			}
		case Remove:
			removed++
		case Unchanged:
			unchanged++
		case Record:
			recorded++
		}
	}
	return
}

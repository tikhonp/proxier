// Package access is what a member may do: a level in each area
// (docs/processes/platform/members.md). Areas follow the module boundaries,
// and each has its own ladder of levels; a higher level includes the lower
// ones. Strings from forms and the database become an Area or a Level only
// through ParseArea and ParseLevel.
package access

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Area is a part of Proxier that a member has one level in.
type Area string

const (
	Servers       Area = "servers"       // servers, templates, rollouts, health
	Subscriptions Area = "subscriptions" // subscriptions, links, alerts, cut-offs
	Routing       Area = "routing"       // lists, services, routers, Shadowrocket, discovery, imports
	RouterScripts Area = "routerscripts" // scripts, versions, generations
	Activity      Area = "activity"      // the Activity and Jobs pages
	Settings      Area = "settings"      // Settings and Members
)

// EveryMember is not an area: a page or an entry that declares it (the
// dashboard, a member's own settings) is open to every member whatever
// their grant.
const EveryMember Area = "every_member"

// Level is how far a member goes in one area.
type Level string

const (
	LevelNone      Level = "none"
	LevelView      Level = "view"       // servers
	LevelManage    Level = "manage"     // servers, settings
	LevelOwn       Level = "own"        // create things and see and change only one's own
	LevelViewAll   Level = "view_all"   // own, plus read everyone's
	LevelManageAll Level = "manage_all" // everything in the area
	LevelAll       Level = "all"        // activity: everyone's events and jobs
)

// ladders holds each area's levels from lowest to highest, in the order the
// UI lists the areas.
var ladders = []struct {
	area   Area
	levels []Level
}{
	{Servers, []Level{LevelNone, LevelView, LevelManage}},
	{Subscriptions, []Level{LevelNone, LevelOwn, LevelViewAll, LevelManageAll}},
	{Routing, []Level{LevelNone, LevelOwn, LevelViewAll, LevelManageAll}},
	{RouterScripts, []Level{LevelNone, LevelOwn, LevelViewAll, LevelManageAll}},
	{Activity, []Level{LevelNone, LevelOwn, LevelAll}},
	{Settings, []Level{LevelNone, LevelManage}},
}

const areaCount = 6

// Areas lists every area in the UI's order.
func Areas() []Area {
	out := make([]Area, len(ladders))
	for i, l := range ladders {
		out[i] = l.area
	}
	return out
}

func (a Area) index() int {
	for i, l := range ladders {
		if l.area == a {
			return i
		}
	}
	return -1
}

// Valid reports whether a is one of the areas (EveryMember is not).
func (a Area) Valid() bool { return a.index() >= 0 }

// Levels lists the area's levels, lowest first.
func (a Area) Levels() []Level {
	i := a.index()
	if i < 0 {
		return nil
	}
	return append([]Level(nil), ladders[i].levels...)
}

// rank is l's position on a's ladder, -1 when l isn't one of a's levels.
func (a Area) rank(l Level) int {
	for i, x := range a.Levels() {
		if x == l {
			return i
		}
	}
	return -1
}

// Top is the area's highest level.
func (a Area) Top() Level {
	ls := a.Levels()
	return ls[len(ls)-1]
}

// ViewLevel is the lowest level that sees the area's pages.
func (a Area) ViewLevel() Level { return a.Levels()[1] }

// ManageLevel is the lowest level that changes anything in the area. In the
// owned areas that is own: a member at own creates and changes their own
// things. Activity has nothing of its own to change, so it is the top.
func (a Area) ManageLevel() Level {
	switch a {
	case Servers, Settings:
		return LevelManage
	case Subscriptions, Routing, RouterScripts:
		return LevelOwn
	}
	return a.Top()
}

// AreaOfModule is the area of a module's jobs and events: the four modules
// are areas of their own name, and the platform's belong to Settings.
func AreaOfModule(module string) Area {
	if a := Area(module); a.Valid() {
		return a
	}
	return Settings
}

var ErrInvalid = errors.New("access: invalid")

// ParseArea reads an area from a form or the database.
func ParseArea(s string) (Area, error) {
	if a := Area(s); a.Valid() {
		return a, nil
	}
	return "", fmt.Errorf("%w area %q", ErrInvalid, s)
}

// ParseLevel reads a level of area a.
func ParseLevel(a Area, s string) (Level, error) {
	if a.rank(Level(s)) < 0 {
		return "", fmt.Errorf("%w level %q for %s", ErrInvalid, s, a)
	}
	return Level(s), nil
}

// Grant is a member's level in every area. The zero Grant is none everywhere.
type Grant struct{ levels [areaCount]Level }

// Level is the grant's level in a; none for an area it doesn't name.
func (g Grant) Level(a Area) Level {
	i := a.index()
	if i < 0 || g.levels[i] == "" {
		return LevelNone
	}
	return g.levels[i]
}

// With returns g with a at l. It panics on a level that isn't a's: levels
// come from ParseLevel or the constants of the area's ladder.
func (g Grant) With(a Area, l Level) Grant {
	if a.rank(l) < 0 {
		panic(fmt.Sprintf("access: %q is not a level of %s", l, a))
	}
	// none is kept as the zero value, so equal grants compare equal
	if l == LevelNone {
		l = ""
	}
	g.levels[a.index()] = l
	return g
}

// Can reports whether the grant reaches at least min in a. Every grant can
// EveryMember; nothing can an unknown area.
func (g Grant) Can(a Area, min Level) bool {
	if a == EveryMember {
		return true
	}
	need := a.rank(min)
	return need >= 0 && a.rank(g.Level(a)) >= need
}

// Sees reports whether the grant shows a's pages: EveryMember, or a's
// lowest level. Navigation, search, the dashboard and Settings show what a
// member sees and nothing else.
func (g Grant) Sees(a Area) bool {
	return a == EveryMember || a.Valid() && g.Can(a, a.ViewLevel())
}

// Reaches is Sees for a page that needs l (Can), or the area's lowest
// level when l is "".
func (g Grant) Reaches(a Area, l Level) bool {
	if l == "" || a == EveryMember {
		return g.Sees(a)
	}
	return g.Can(a, l)
}

// Within is g with every area lowered to limit's level where limit's is
// lower: the most of g a member with limit can give.
func (g Grant) Within(limit Grant) Grant {
	for _, a := range Areas() {
		if a.rank(limit.Level(a)) < a.rank(g.Level(a)) {
			g = g.With(a, limit.Level(a))
		}
	}
	return g
}

// Overreach lists the areas where moving a member from one grant to
// another raises them above by, the grant of whoever makes the change: a
// member can't give anyone a level above their own. A level left as it was
// is no overreach, so a member can still change someone above them in the
// other areas.
func Overreach(by, from, to Grant) []Area {
	var out []Area
	for _, a := range Areas() {
		t := a.rank(to.Level(a))
		if t > a.rank(from.Level(a)) && t > a.rank(by.Level(a)) {
			out = append(out, a)
		}
	}
	return out
}

// String writes the grant as "servers=view,subscriptions=own,…", every area
// in order: the form of event payloads.
func (g Grant) String() string {
	parts := make([]string, 0, areaCount)
	for _, a := range Areas() {
		parts = append(parts, string(a)+"="+string(g.Level(a)))
	}
	return strings.Join(parts, ",")
}

// Changes lists what differs between two grants as "servers: view → manage",
// for event payloads.
func Changes(from, to Grant) []string {
	var out []string
	for _, a := range Areas() {
		if f, t := from.Level(a), to.Level(a); f != t {
			out = append(out, string(a)+": "+string(f)+" → "+string(t))
		}
	}
	return out
}

// Preset is a named grant: Owner, Operator or Viewer.
type Preset string

const (
	PresetOwner    Preset = "owner"
	PresetOperator Preset = "operator"
	PresetViewer   Preset = "viewer"
)

// Presets lists the presets in the order the UI offers them.
func Presets() []Preset { return []Preset{PresetOwner, PresetOperator, PresetViewer} }

// Grant is the preset's grant: Owner is the top level everywhere, Operator
// is Owner without Settings, Viewer reads everything and changes nothing
// but their own things.
func (p Preset) Grant() Grant {
	var g Grant
	for _, a := range Areas() {
		switch p {
		case PresetOwner:
			g = g.With(a, a.Top())
		case PresetOperator:
			if a != Settings {
				g = g.With(a, a.Top())
			}
		case PresetViewer:
			switch a {
			case Servers:
				g = g.With(a, LevelView)
			case Activity:
				g = g.With(a, LevelAll)
			case Settings:
			default:
				g = g.With(a, LevelViewAll)
			}
		}
	}
	return g
}

// PresetOf names the preset g equals, "" for a custom grant.
func PresetOf(g Grant) Preset {
	for _, p := range Presets() {
		if p.Grant() == g {
			return p
		}
	}
	return ""
}

// Who is a signed-in member as access sees them: their ID and grant.
type Who struct {
	Member int64
	Grant  Grant
}

// Scope is whose things the member reaches in a: at own their own; at
// view_all they read everyone's and change their own; at manage_all (and in
// an area with no owners, servers or settings) everything. Activity's all
// reads everyone's.
func (w Who) Scope(a Area) Scope {
	s := Owner(w.Member)
	switch l := w.Grant.Level(a); {
	case l == LevelViewAll:
		s.readsAll = true
	case l == LevelManageAll, l == LevelAll, a == Servers, a == Settings:
		return All
	}
	return s
}

// Scope is whose things a query reaches: everyone's, or one member's own,
// and separately for reading and for changing. Every query of owned things
// takes one, so a forgotten scope doesn't compile. Handlers take it from
// the request (web.Scope); jobs and event subscribers use All.
//
// A Scope is a SQL argument standing for the owner whose rows it reads, 0
// for everyone's: a query keeps a row with "? IN (0, owner_id)". The zero
// Scope reads nothing, so a Scope never made by All, Owner or Who.Scope
// can't widen a query.
type Scope struct {
	member              int64
	readsAll, writesAll bool
}

// All reads and changes everyone's things.
var All = Scope{readsAll: true, writesAll: true}

// Owner reads and changes the things of one member.
func Owner(member int64) Scope { return Scope{member: member} }

// Member is the member whose things the scope reads; ok is false when it
// reads everyone's.
func (s Scope) Member() (member int64, ok bool) { return s.member, !s.readsAll }

// Reads reports whether the scope reaches a thing of owner.
func (s Scope) Reads(owner int64) bool { return s.readsAll || s.member != 0 && owner == s.member }

// Writes reports whether the scope may change a thing of owner.
func (s Scope) Writes(owner int64) bool { return s.writesAll || s.member != 0 && owner == s.member }

// Writable is the scope narrowed to what it may change: for loading a
// thing to change it, or offering things to pick (a subscription for a new
// link). A thing the scope reads but can't change is then not found.
func (s Scope) Writable() Scope {
	return Scope{member: s.member, readsAll: s.writesAll, writesAll: s.writesAll}
}

// Value makes a Scope a SQL argument: 0 for everyone's, the member's ID,
// or -1 (no owner has it) for a zero Scope.
func (s Scope) Value() (driver.Value, error) {
	switch {
	case s.readsAll:
		return int64(0), nil
	case s.member == 0:
		return int64(-1), nil
	}
	return s.member, nil
}

func (s Scope) String() string {
	switch {
	case s.readsAll && s.writesAll:
		return "all"
	case s.readsAll:
		return "read all, owner:" + strconv.FormatInt(s.member, 10)
	}
	return "owner:" + strconv.FormatInt(s.member, 10)
}

// ForbiddenError is a member's grant not reaching what they asked for in
// Area: a route of a higher level, or a thing they read but may not change.
// The web error handler answers it with a 403 page naming the area, or for
// htmx (which swaps no 4xx) a fragment the key line shows.
type ForbiddenError struct{ Area Area }

func (e *ForbiddenError) Error() string {
	return "access: " + string(e.Area) + " doesn't reach this"
}

// StatusCode is 403 (echo reads it).
func (e *ForbiddenError) StatusCode() int { return 403 }

// Forbidden is the error of a request the member's grant doesn't reach in
// area. Being an error, it stops a handler or service that checks more than
// its route.
func Forbidden(area Area) error { return &ForbiddenError{Area: area} }

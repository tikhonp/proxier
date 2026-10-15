package access

import (
	"errors"
	"slices"
	"testing"
)

func TestParseRefusesLevelsOfAnotherArea(t *testing.T) {
	if _, err := ParseLevel(Servers, "own"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("servers own: %v", err)
	}
	if _, err := ParseLevel(Activity, "manage"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("activity manage: %v", err)
	}
	if l, err := ParseLevel(Routing, "view_all"); err != nil || l != LevelViewAll {
		t.Fatalf("routing view_all: %v %v", l, err)
	}
	if _, err := ParseArea("every_member"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("every_member parsed as an area: %v", err)
	}
	if _, err := ParseArea(""); !errors.Is(err, ErrInvalid) {
		t.Fatal("an empty area parsed")
	}
}

func TestHigherLevelsIncludeLowerOnes(t *testing.T) {
	g := Grant{}.With(Subscriptions, LevelViewAll).With(Servers, LevelView)
	cases := []struct {
		area Area
		min  Level
		want bool
	}{
		{Subscriptions, LevelOwn, true},
		{Subscriptions, LevelViewAll, true},
		{Subscriptions, LevelManageAll, false},
		{Servers, LevelView, true},
		{Servers, LevelManage, false},
		{Routing, LevelOwn, false},
		{Settings, LevelManage, false},
		{EveryMember, LevelNone, true},
		{Area("nope"), LevelNone, false},
		// a level of another ladder never matches
		{Servers, LevelOwn, false},
	}
	for _, c := range cases {
		if got := g.Can(c.area, c.min); got != c.want {
			t.Errorf("Can(%s, %s) = %v, want %v", c.area, c.min, got, c.want)
		}
	}
	if (Grant{}).Level(Servers) != LevelNone {
		t.Fatal("the zero grant isn't none")
	}
	// a grant read from a form names none; one read from rows leaves it out
	if (Grant{}).With(Settings, LevelNone) != (Grant{}) || PresetOf(PresetViewer.Grant().With(Settings, LevelNone)) != PresetViewer {
		t.Fatal("none set explicitly differs from none left out")
	}
}

func TestPresetsFillTheGrid(t *testing.T) {
	owner, op, viewer := PresetOwner.Grant(), PresetOperator.Grant(), PresetViewer.Grant()
	for _, a := range Areas() {
		if owner.Level(a) != a.Top() {
			t.Errorf("owner %s = %s", a, owner.Level(a))
		}
	}
	if op.Level(Settings) != LevelNone || op.Level(Servers) != LevelManage || op.Level(Routing) != LevelManageAll {
		t.Errorf("operator = %s", op)
	}
	want := "servers=view,subscriptions=view_all,routing=view_all,routerscripts=view_all,activity=all,settings=none"
	if viewer.String() != want {
		t.Errorf("viewer = %s", viewer)
	}
	if PresetOf(op) != PresetOperator || PresetOf(op.With(Servers, LevelView)) != "" {
		t.Error("PresetOf doesn't name the grid")
	}
}

func TestOverreachIsARaiseAboveTheEditor(t *testing.T) {
	editor := Grant{}.With(Servers, LevelView).With(Settings, LevelManage)
	from := Grant{}.With(Servers, LevelManage)
	// keeping someone's higher level, or lowering it, is no overreach
	if got := Overreach(editor, from, from.With(Settings, LevelManage)); len(got) != 0 {
		t.Fatalf("kept level counted: %v", got)
	}
	if got := Overreach(editor, from, from.With(Servers, LevelView)); len(got) != 0 {
		t.Fatalf("lowering counted: %v", got)
	}
	to := Grant{}.With(Servers, LevelView).With(Routing, LevelOwn)
	if got := Overreach(editor, Grant{}, to); !slices.Equal(got, []Area{Routing}) {
		t.Fatalf("raise above the editor = %v, want routing", got)
	}
}

func TestChangesNameEveryAreaThatMoved(t *testing.T) {
	from := PresetViewer.Grant()
	to := from.With(Servers, LevelManage).With(Activity, LevelOwn)
	if got := Changes(from, to); !slices.Equal(got, []string{"servers: view → manage", "activity: all → own"}) {
		t.Fatalf("changes = %v", got)
	}
}

func TestWithPanicsOnALevelOfAnotherArea(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	_ = Grant{}.With(Settings, LevelView)
}

func TestModulesAreAreasAndThePlatformIsSettings(t *testing.T) {
	for m, want := range map[string]Area{"servers": Servers, "routerscripts": RouterScripts, "platform": Settings} {
		if got := AreaOfModule(m); got != want {
			t.Errorf("%s → %s", m, got)
		}
	}
}

func TestWithinLowersToTheLimit(t *testing.T) {
	limit := Grant{}.With(Servers, LevelView).With(Subscriptions, LevelManageAll)
	got := PresetViewer.Grant().Within(limit)
	want := Grant{}.With(Servers, LevelView).With(Subscriptions, LevelViewAll)
	if got != want {
		t.Fatalf("viewer within %s = %s, want %s", limit, got, want)
	}
}

func TestScopesReadAndChangeByLevel(t *testing.T) {
	const me, other = 7, 9
	who := func(l Level) Who { return Who{Member: me, Grant: Grant{}.With(Routing, l)} }
	cases := []struct {
		level                  Level
		readsOther, writeOther bool
	}{
		{LevelOwn, false, false},
		{LevelViewAll, true, false},
		{LevelManageAll, true, true},
	}
	for _, c := range cases {
		s := who(c.level).Scope(Routing)
		if !s.Reads(me) || !s.Writes(me) {
			t.Errorf("%s: doesn't reach the member's own things", c.level)
		}
		if s.Reads(other) != c.readsOther || s.Writes(other) != c.writeOther {
			t.Errorf("%s: reads another's %v, changes another's %v", c.level, s.Reads(other), s.Writes(other))
		}
		if w := s.Writable(); w.Reads(other) != c.writeOther || !w.Reads(me) {
			t.Errorf("%s: Writable reads another's %v", c.level, w.Reads(other))
		}
	}
	if s := (Who{Member: me, Grant: PresetViewer.Grant()}).Scope(Subscriptions); !s.Reads(other) || s.Writes(other) {
		t.Error("a Viewer must read everyone's subscriptions and change only their own")
	}
	if s := (Who{Member: me}).Scope(Servers); s != All {
		t.Error("servers have no owners: every scope there is All")
	}
	if s := (Who{Member: me, Grant: Grant{}.With(Activity, LevelOwn)}).Scope(Activity); s.Reads(other) {
		t.Error("activity: own reads another member's")
	}
}

func TestScopeIsTheOwnerAsASQLArgument(t *testing.T) {
	cases := []struct {
		s    Scope
		want int64
	}{
		{All, 0},
		{Owner(7), 7},
		{Who{Member: 7, Grant: Grant{}.With(Routing, LevelViewAll)}.Scope(Routing), 0},
		{Who{Member: 7, Grant: Grant{}.With(Routing, LevelViewAll)}.Scope(Routing).Writable(), 7},
		{Scope{}, -1}, // a Scope made by nothing reads nothing
		{Owner(0), -1},
	}
	for _, c := range cases {
		v, err := c.s.Value()
		if err != nil || v != c.want {
			t.Errorf("%s: %v %v, want %d", c.s, v, err, c.want)
		}
	}
	if (Scope{}).Reads(0) || (Scope{}).Writes(0) {
		t.Error("the zero Scope reaches the things of member 0")
	}
}

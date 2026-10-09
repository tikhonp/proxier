package routers

import (
	"slices"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
)

var n = routeros.Defaults

func sfx(names ...string) []routeros.Entry {
	out := make([]routeros.Entry, 0, len(names))
	for _, x := range names {
		out = append(out, routeros.Entry{Name: x})
	}
	return out
}

// router reads entries installed as a block installs them.
func router(tags map[string][]routeros.Entry, extra ...routeros.DNSEntry) State {
	var dns []routeros.DNSEntry
	var list []routeros.ListEntry
	for tag, es := range tags {
		for _, e := range es {
			dns = append(dns, routeros.DNSEntry{Comment: tag, Name: e.Name, Type: "FWD", ForwardTo: n.Forwarder, Subdomain: !e.Exact})
			list = append(list, routeros.ListEntry{Comment: tag, Address: e.Name})
		}
	}
	dns = append(dns, extra...)
	list = append(list, routeros.ListEntry{Comment: "mtvpn:doh", Address: "dns.google"}, routeros.ListEntry{Comment: "telegram-cidr", Address: "91.108.4.0/22"})
	return Interpret(dns, list, n, time.Now())
}

func applied(e []routeros.Entry) Applied { return Applied{Hash: routeros.EntriesHash(e)} }

func byTag(plan []TagPlan) map[string]TagPlan {
	m := map[string]TagPlan{}
	for _, p := range plan {
		m[p.Tag] = p
	}
	return m
}

func TestPlanTable(t *testing.T) {
	want := []Want{
		{Tag: "a", Entries: sfx("a.com")},
		{Tag: "b", Entries: sfx("b.com")},
		{Tag: "c", Entries: sfx("c.com")},
		{Tag: "d", Entries: sfx("d-new.com")},
		{Tag: "e", Entries: sfx("e.com")},
		{Tag: "h"}, // owns nothing in the list
		{Tag: "j"}, // owns nothing, nothing on the router, applied once
		{Tag: "k"}, // owns nothing, never anywhere
	}
	st := router(map[string][]routeros.Entry{
		"a": sfx("a.com"), "b": sfx("b.com"), "d": sfx("d-old.com"),
		"f": sfx("f.com"), "g": sfx("g.com"), "h": sfx("h.com"), "x": sfx("x.com"),
	})
	app := map[string]Applied{
		"a": applied(sfx("a.com")), "d": applied(sfx("d-old.com")), "e": applied(sfx("e.com")),
		"f": applied(sfx("f.com")), "i": applied(sfx("i.com")), "j": applied(sfx("j.com")),
	}
	plan := MakePlan(want, st, app, Extra{Remove: []string{"x"}})
	got := byTag(plan)
	for tag, w := range map[string][2]string{
		"a": {Unchanged, ""},
		"b": {Record, ""},
		"c": {Update, WhyNew},
		"d": {Update, WhyChanged},
		"e": {Update, WhyDrift},
		"f": {Remove, WhyLeftList},
		"g": {Unmanaged, WhyNeverInstalled},
		"h": {Remove, WhyOwnsNothing},
		"i": {Forget, ""},
		"j": {Forget, ""},
		"k": {Unchanged, ""},
		"x": {Remove, WhyAsked},
	} {
		p, ok := got[tag]
		if !ok || p.Action != w[0] || p.Why != w[1] {
			t.Errorf("%s: %+v, want %v", tag, p, w)
		}
	}
	if len(got) != 12 {
		t.Fatalf("rows: %d (%v)", len(got), plan)
	}
	if _, ok := got["telegram-cidr"]; ok {
		t.Fatal("an address-list-only comment is never a tag")
	}
	if got["g"].Want != -1 || got["g"].Have != 1 || got["c"].Want != 1 || got["c"].Have != 0 {
		t.Fatalf("counts: %+v %+v", got["g"], got["c"])
	}
	// removing the router removes every tag on it, desired or not, and
	// forgets what is gone
	all := byTag(MakePlan(want, st, app, Extra{RemoveAll: true}))
	for _, tag := range []string{"a", "b", "d", "f", "g", "h", "x"} {
		if all[tag].Action != Remove {
			t.Errorf("remove all: %s is %+v", tag, all[tag])
		}
	}
	if all["e"].Action != Forget || all["i"].Action != Forget {
		t.Errorf("remove all forgets: %+v %+v", all["e"], all["i"])
	}
	// pushes only for updates and removals
	var blocks []string
	for _, b := range Blocks(plan) {
		blocks = append(blocks, b.Tag)
	}
	slices.Sort(blocks)
	if !slices.Equal(blocks, []string{"c", "d", "e", "f", "h", "x"}) {
		t.Fatalf("blocks: %v", blocks)
	}
}

func TestStrictMatch(t *testing.T) {
	want := []routeros.Entry{{Name: "a.com"}, {Name: "api.b.com", Exact: true}}
	good := func() *TagState {
		return &TagState{
			DNS: []routeros.DNSEntry{
				{Comment: "t", Name: "a.com", Type: "FWD", ForwardTo: "vpn-doh", Subdomain: true},
				{Comment: "t", Name: "api.b.com", Type: "FWD", ForwardTo: "vpn-doh"},
			},
			List: []string{"a.com", "api.b.com"},
		}
	}
	if !Matches(good(), want, n) {
		t.Fatal("the same entries must match")
	}
	for what, spoil := range map[string]func(*TagState){
		"forward-to":      func(s *TagState) { s.DNS[0].ForwardTo = "8.8.8.8" },
		"type":            func(s *TagState) { s.DNS[1].Type = "A" },
		"match-subdomain": func(s *TagState) { s.DNS[1].Subdomain = true },
		"missing list":    func(s *TagState) { s.List = s.List[:1] },
		"wrong list name": func(s *TagState) { s.List[1] = "b.com" },
		"duplicate dns":   func(s *TagState) { s.DNS = append(s.DNS, s.DNS[0]) },
		"duplicate list":  func(s *TagState) { s.List = append(s.List, "a.com") },
		"missing dns":     func(s *TagState) { s.DNS = s.DNS[:1] },
	} {
		s := good()
		spoil(s)
		if Matches(s, want, n) {
			t.Errorf("%s: matched", what)
		}
	}
	if !Matches(nil, nil, n) || Matches(nil, want, n) {
		t.Fatal("nothing matches only nothing")
	}
}

func TestPushOrder(t *testing.T) {
	want := []Want{
		{Tag: "anthropic", Position: 1, Entries: sfx("anthropic.com")},    // loses claude.ai: no gain
		{Tag: "mine", Position: 2, Entries: sfx("claude.ai", "mine.org")}, // gains claude.ai
		{Tag: "zeta", Position: 3, Entries: sfx("zeta.com")},              // new: gains
		{Tag: "same", Position: 4, Entries: sfx("same.com")},
	}
	st := router(map[string][]routeros.Entry{
		"anthropic": sfx("anthropic.com", "claude.ai"), "mine": sfx("mine.org"), "same": sfx("same.com"),
		"old": sfx("old.com"), "older": sfx("older.com"),
	})
	app := map[string]Applied{
		"anthropic": applied(sfx("anthropic.com", "claude.ai")), "mine": applied(sfx("mine.org")), "same": applied(sfx("same.com")),
		"old": applied(sfx("old.com")), "older": applied(sfx("older.com")),
	}
	var order []string
	for _, p := range MakePlan(want, st, app, Extra{}) {
		order = append(order, p.Tag+":"+p.Action)
	}
	if !slices.Equal(order, []string{"mine:update", "zeta:update", "anthropic:update", "old:remove", "older:remove", "same:unchanged"}) {
		t.Fatalf("order: %v", order)
	}
}

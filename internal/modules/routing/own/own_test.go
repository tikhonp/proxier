package own

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

// m is a member; names as the editor takes them ("example.com", "full:api.example.com").
func m(id int64, tag string, names ...string) Member {
	var suffix, exact []string
	for _, n := range names {
		if e, ok := strings.CutPrefix(n, "full:"); ok {
			exact = append(exact, e)
		} else {
			suffix = append(suffix, n)
		}
	}
	return Member{ServiceID: id, Tag: tag, Set: snapshot.New(suffix, exact, nil)}
}

func installs(t *testing.T, o Owned, want ...string) {
	t.Helper()
	var got []string
	got = append(got, o.Suffix...)
	for _, e := range o.Exact {
		got = append(got, "full:"+e)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("%s installs %v, want %v", o.Tag, got, want)
	}
}

func dropped(t *testing.T, o Owned, want ...Drop) {
	t.Helper()
	if len(want) == 0 && len(o.Dropped) == 0 {
		return
	}
	if !reflect.DeepEqual(o.Dropped, want) {
		t.Errorf("%s dropped %+v, want %+v", o.Tag, o.Dropped, want)
	}
}

func TestFirstServiceOwnsSharedName(t *testing.T) {
	r := Compute([]Member{m(1, "anthropic", "claude.ai", "anthropic.com"), m(2, "mine", "claude.ai", "example.com")}, nil, nil)
	installs(t, r.Services[0], "anthropic.com", "claude.ai")
	installs(t, r.Services[1], "example.com")
	dropped(t, r.Services[1], Drop{Name: "claude.ai", Reason: ReasonOwned, By: "anthropic"})
	if r.Count() != 3 || r.Services[1].Total != 2 || r.Services[1].Count() != 1 {
		t.Errorf("counts: %d, %d/%d", r.Count(), r.Services[1].Count(), r.Services[1].Total)
	}
}

func TestRemovingOwnerHandsNameOver(t *testing.T) {
	r := Compute([]Member{m(2, "mine", "claude.ai", "example.com")}, nil, nil)
	installs(t, r.Services[0], "claude.ai", "example.com")
	dropped(t, r.Services[0])
}

func TestReorderMovesOwnership(t *testing.T) {
	r := Compute([]Member{m(2, "mine", "claude.ai"), m(1, "anthropic", "claude.ai", "anthropic.com")}, nil, nil)
	installs(t, r.Services[0], "claude.ai")
	installs(t, r.Services[1], "anthropic.com")
	dropped(t, r.Services[1], Drop{Name: "claude.ai", Reason: ReasonOwned, By: "mine"})
}

func TestExactCoveredBySuffix(t *testing.T) {
	r := Compute([]Member{m(1, "anthropic", "anthropic.com"), m(2, "mine", "full:console.anthropic.com", "full:example.com")}, nil, nil)
	installs(t, r.Services[1], "full:example.com")
	dropped(t, r.Services[1], Drop{Name: "console.anthropic.com", Exact: true, Reason: ReasonCovered, By: "anthropic", Via: "anthropic.com"})
}

func TestBroaderSuffixWinsWhateverTheOrder(t *testing.T) {
	want := Drop{Name: "api.example.com", Reason: ReasonCovered, By: "other", Via: "example.com"}
	for _, order := range [][]Member{
		{m(1, "mine", "api.example.com"), m(2, "other", "example.com")},
		{m(2, "other", "example.com"), m(1, "mine", "api.example.com")},
	} {
		r := Compute(order, nil, nil)
		for _, o := range r.Services {
			if o.Tag == "mine" {
				installs(t, o)
				dropped(t, o, want)
			} else {
				installs(t, o, "example.com")
			}
		}
	}
	r := Compute([]Member{m(1, "mine", "api.example.com")}, nil, nil)
	installs(t, r.Services[0], "api.example.com")
}

func TestSuffixBeatsExactOfSameName(t *testing.T) {
	r := Compute([]Member{m(1, "anthropic", "full:claude.ai"), m(2, "mine", "claude.ai")}, nil, nil)
	installs(t, r.Services[0])
	dropped(t, r.Services[0], Drop{Name: "claude.ai", Exact: true, Reason: ReasonCovered, By: "mine", Via: "claude.ai"})
	installs(t, r.Services[1], "claude.ai")
}

func TestRedundancyInsideServiceKept(t *testing.T) {
	r := Compute([]Member{m(1, "google", "google.com", "mail.google.com", "full:docs.google.com")}, nil, nil)
	installs(t, r.Services[0], "google.com", "mail.google.com", "full:docs.google.com")
	dropped(t, r.Services[0])

	// an earlier service listing a name the later one has under its own
	// suffix: the broader suffix covers it, and the later keeps its own copy
	r = Compute([]Member{m(1, "mail", "mail.google.com"), m(2, "google", "google.com", "mail.google.com")}, nil, nil)
	installs(t, r.Services[0])
	installs(t, r.Services[1], "google.com", "mail.google.com")
}

func TestGuardedNamesCoverNothing(t *testing.T) {
	srv := []Server{{Name: "nl-1", Hostnames: []string{"nl-1.hosts.tikhonnnnn.com"}}}
	r := Compute([]Member{m(1, "mine", "tikhonnnnn.com", "example.com"), m(2, "api", "full:api.tikhonnnnn.com")}, srv, nil)
	installs(t, r.Services[0], "example.com")
	dropped(t, r.Services[0], Drop{Name: "tikhonnnnn.com", Reason: ReasonGuarded, By: "nl-1", Via: "nl-1.hosts.tikhonnnnn.com"})
	installs(t, r.Services[1], "full:api.tikhonnnnn.com")
	if g := r.Guarded(); len(g) != 1 || g[0].Tag != "mine" || g[0].Name != "tikhonnnnn.com" {
		t.Errorf("guarded: %+v", g)
	}
}

func TestExcludedNames(t *testing.T) {
	r := Compute([]Member{m(1, "google", "google.com", "dns.google"), m(2, "other", "full:dns.google")}, nil, map[string]string{"dns.google": "mtvpn:doh"})
	installs(t, r.Services[0], "google.com")
	dropped(t, r.Services[0], Drop{Name: "dns.google", Reason: ReasonPinned, Via: "mtvpn:doh"})
	installs(t, r.Services[1])
	dropped(t, r.Services[1], Drop{Name: "dns.google", Exact: true, Reason: ReasonPinned, Via: "mtvpn:doh"})
}

func TestCoveringAndCoveredBy(t *testing.T) {
	srv := []Server{{Name: "de-1", Hostnames: []string{"de-1.example.net"}}, {Name: "nl-1", Hostnames: []string{"nl-1.hosts.tikhonnnnn.com", "proxy.tikhonnnnn.com"}}}
	for _, c := range []struct {
		name   string
		exact  bool
		server string
		host   string
	}{
		{"tikhonnnnn.com", false, "nl-1", "nl-1.hosts.tikhonnnnn.com"},
		{"hosts.tikhonnnnn.com", false, "nl-1", "nl-1.hosts.tikhonnnnn.com"},
		{"nl-1.hosts.tikhonnnnn.com", false, "nl-1", "nl-1.hosts.tikhonnnnn.com"},
		{"nl-1.hosts.tikhonnnnn.com", true, "nl-1", "nl-1.hosts.tikhonnnnn.com"},
		{"proxy.tikhonnnnn.com", true, "nl-1", "proxy.tikhonnnnn.com"},
		{"tikhonnnnn.com", true, "", ""},
		{"x.nl-1.hosts.tikhonnnnn.com", false, "", ""},
		{"ikhonnnnn.com", false, "", ""},
		{"example.net", false, "de-1", "de-1.example.net"},
	} {
		s, h, ok := Covering(c.name, c.exact, srv)
		if s != c.server || h != c.host || ok != (c.server != "") {
			t.Errorf("Covering(%s, %v) = %s %s %v", c.name, c.exact, s, h, ok)
		}
	}
	if _, _, ok := Covering("tikhonnnnn.com", false, nil); ok {
		t.Error("no servers, nothing covered")
	}

	members := []Member{m(1, "openai", "openai.com", "full:chatgpt.com"), m(2, "mine", "api.openai.com"), m(3, "big", "x.api.openai.com", "chatgpt.com")}
	got := CoveredBy("api.openai.com", false, members, 2)
	want := []Cover{{Tag: "openai", Via: "openai.com"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CoveredBy: %+v", got)
	}
	if got := CoveredBy("chatgpt.com", true, members, 2); !reflect.DeepEqual(got, []Cover{{Tag: "openai", Via: "chatgpt.com", Exact: true}, {Tag: "big", Via: "chatgpt.com"}}) {
		t.Errorf("CoveredBy exact: %+v", got)
	}
	if got := CoveredBy("chatgpt.com", false, members[:2], 2); len(got) != 0 {
		t.Errorf("an exact name doesn't cover a suffix: %+v", got)
	}
	if got := CoveredBy("example.org", false, members, 0); len(got) != 0 {
		t.Errorf("nothing covers example.org: %+v", got)
	}
}

func TestMovedNames(t *testing.T) {
	yt := m(1, "youtube", "youtube.com", "googlevideo.com", "ytimg.com")
	g := m(2, "google", "google.com", "googlevideo.com", "ytimg.com", "full:accounts.google.com")
	before := Compute([]Member{yt, g}, nil, nil)
	after := Compute([]Member{g, yt}, nil, nil)
	moved := Moved(before, after)
	want := []Move{{Name: "googlevideo.com", From: "youtube", To: "google"}, {Name: "ytimg.com", From: "youtube", To: "google"}}
	if !reflect.DeepEqual(moved, want) {
		t.Errorf("moved: %+v", moved)
	}
	// a reorder of services that share nothing moves nothing
	a := m(3, "a", "a.com")
	b := m(4, "b", "b.com")
	if got := Moved(Compute([]Member{a, b}, nil, nil), Compute([]Member{b, a}, nil, nil)); len(got) != 0 {
		t.Errorf("moved: %+v", got)
	}
	// a name that leaves or arrives is a move to or from nobody
	got := Moved(Compute([]Member{a}, nil, nil), Compute([]Member{b}, nil, nil))
	if !reflect.DeepEqual(got, []Move{{Name: "a.com", From: "a"}, {Name: "b.com", To: "b"}}) {
		t.Errorf("moved: %+v", got)
	}
}

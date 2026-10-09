package shadowrocket_test

import (
	"reflect"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

func view(members ...own.Member) lists.View {
	return lists.View{Result: own.Compute(members, nil, nil)}
}

func member(id int64, tag string, suffix, exact []string) own.Member {
	return own.Member{ServiceID: id, Tag: tag, Set: snapshot.New(suffix, exact, nil)}
}

func TestOwnershipInRules(t *testing.T) {
	got := shadowrocket.Rules(view(
		member(1, "anthropic", []string{"anthropic.com"}, nil),
		member(2, "mine", nil, []string{"console.anthropic.com"}),
	))
	want := []shadowrocket.Block{{Tag: "anthropic", Rules: []shadowrocket.Rule{{Name: "anthropic.com"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	// a name two services share stays with the first; DOMAIN-SUFFIX sorted, then DOMAIN sorted
	got = shadowrocket.Rules(view(
		member(1, "google", []string{"youtube.com", "google.com"}, []string{"z.example.org", "a.example.net"}),
		member(2, "youtube", []string{"youtube.com", "ytimg.com"}, nil),
	))
	want = []shadowrocket.Block{
		{Tag: "google", Rules: []shadowrocket.Rule{{Name: "google.com"}, {Name: "youtube.com"}, {Name: "a.example.net", Exact: true}, {Name: "z.example.org", Exact: true}}},
		{Tag: "youtube", Rules: []shadowrocket.Rule{{Name: "ytimg.com"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shared:\n%+v", got)
	}
	if shadowrocket.Count(got) != 5 {
		t.Errorf("count %d", shadowrocket.Count(got))
	}
}

func TestRulesDropCoveredInsideService(t *testing.T) {
	v := view(member(1, "google", []string{"google.com", "mail.google.com"}, []string{"google.com", "api.google.com", "other.org"}))
	// on routers the service keeps its own covered names
	if o := v.Result.Services[0]; len(o.Suffix) != 2 {
		t.Fatalf("own dropped inside the service: %+v", o)
	}
	got := shadowrocket.Rules(v)
	want := []shadowrocket.Block{{Tag: "google", Rules: []shadowrocket.Rule{{Name: "google.com"}, {Name: "other.org", Exact: true}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

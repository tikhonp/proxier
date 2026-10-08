package sources

import (
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

func TestParseList(t *testing.T) {
	l := ParseList(`# Netflix
domain:Netflix.com   # the main one
nflxvideo.net @cdn
full:api.netflix.com @ads @cn
regexp:^nflx.*\.net$
keyword:netflix
include:netflix-cdn @cdn @-cn
bad..name
exa_mple.com
also.ok &affiliated
   
`)
	want := []Entry{
		{Name: "netflix.com"},
		{Name: "nflxvideo.net", Attrs: []string{"cdn"}},
		{Name: "api.netflix.com", Exact: true, Attrs: []string{"ads", "cn"}},
		{Name: "also.ok"},
	}
	if !slices.EqualFunc(l.Entries, want, func(a, b Entry) bool {
		return a.Name == b.Name && a.Exact == b.Exact && slices.Equal(a.Attrs, b.Attrs)
	}) {
		t.Errorf("entries: %+v", l.Entries)
	}
	if len(l.Includes) != 1 || l.Includes[0].Name != "netflix-cdn" || !slices.Equal(l.Includes[0].With, []string{"cdn"}) ||
		!slices.Equal(l.Includes[0].Without, []string{"cn"}) {
		t.Errorf("includes: %+v", l.Includes)
	}
	wantSkipped := []snapshot.Skipped{
		{Entry: `regexp:^nflx.*\.net$`, Reason: SkipUnsupported},
		{Entry: "keyword:netflix", Reason: SkipUnsupported},
		{Entry: "bad..name", Reason: SkipInvalid},
		{Entry: "exa_mple.com", Reason: SkipInvalid},
	}
	if !slices.Equal(l.Skipped, wantSkipped) {
		t.Errorf("skipped: %+v", l.Skipped)
	}
}

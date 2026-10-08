package sources_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/sources/sourcestest"
)

func TestIncludesAndFilters(t *testing.T) {
	up := sourcestest.New(t)
	up.V2fly("netflix", "netflix.com\ninclude:netflix-cdn\ninclude:shared\n")
	up.V2fly("netflix-cdn", "nflxvideo.net\nfull:x.nflxvideo.net\ninclude:netflix\ninclude:shared\nregexp:x\n") // a cycle; shared twice
	up.V2fly("shared", "shared.com\n")
	r := resolver(up)
	got, err := r.Resolve(context.Background(), parse(t, "v2fly:netflix"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Set.Suffix, []string{"netflix.com", "nflxvideo.net", "shared.com"}) || !slices.Equal(got.Set.Exact, []string{"x.nflxvideo.net"}) {
		t.Errorf("set: %+v", got.Set)
	}
	if len(got.Set.Skipped) != 1 || got.Set.Skipped[0].Entry != "regexp:x" {
		t.Errorf("skipped of an included list: %+v", got.Set.Skipped)
	}
	if c := up.Requests("/v2fly/data/"); c != 3 {
		t.Errorf("%d fetches for three lists", c)
	}

	up.V2fly("cat", "plain.com\nads.com @ads\nadscn.com @ads @cn\ncn.com @cn\n")
	up.V2fly("only-ads", "include:cat @ads\n")
	up.V2fly("not-cn", "include:cat @-cn\n")
	up.V2fly("ads-not-cn", "include:cat @ads @-cn\n")
	for sel, want := range map[string][]string{
		"only-ads":   {"ads.com", "adscn.com"},
		"not-cn":     {"ads.com", "plain.com"},
		"ads-not-cn": {"ads.com"},
	} {
		got, err := r.Resolve(context.Background(), parse(t, sel))
		if err != nil || !slices.Equal(got.Set.Suffix, want) {
			t.Errorf("%s: %v %v, want %v", sel, got.Set.Suffix, err, want)
		}
	}
}

func TestMissingListAndInclude(t *testing.T) {
	up := sourcestest.New(t)
	r := resolver(up)
	_, err := r.Resolve(context.Background(), parse(t, "v2fly:nosuchlist"))
	if !errors.Is(err, sources.ErrNotFound) || !strings.Contains(err.Error(), "v2fly has no list 'nosuchlist'") {
		t.Errorf("missing list: %v", err)
	}
	up.V2fly("geolocation-!cn", "a.com\ninclude:geolocation-cn\n")
	_, err = r.Resolve(context.Background(), parse(t, "v2fly:geolocation-!cn"))
	var ie *sources.IncludeError
	if errors.Is(err, sources.ErrNotFound) || !errors.As(err, &ie) || err.Error() != "include:geolocation-cn: HTTP 404" {
		t.Errorf("missing include: %v", err)
	}
	up.V2fly("geolocation-cn", "b.com\n")
	up.Fail("/v2fly/data/geolocation-cn", 500)
	if _, err := r.Resolve(context.Background(), parse(t, "v2fly:geolocation-!cn")); err == nil || err.Error() != "include:geolocation-cn: HTTP 500" {
		t.Errorf("failing include: %v", err)
	}
}

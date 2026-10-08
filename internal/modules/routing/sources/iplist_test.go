package sources_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/sources/sourcestest"
)

func TestPortalsInOrder(t *testing.T) {
	ctx := context.Background()
	up := sourcestest.New(t)
	up.Site("main", "video", "youtube.com", "youtube.com", "ytimg.com", "googlevideo.com", "<junk>")
	up.Site("beta", "apple", "apple.com", "apple.com")
	up.Site("beta", "apple", "icloud.com", "icloud.com")
	up.Site("main", "other", "apple-ish.com", "unrelated.com")
	r := resolver(up)

	got, err := r.Resolve(ctx, parse(t, "iplist:youtube.com"))
	if err != nil || got.Portal != "main" || got.Kind != "site" || !slices.Equal(got.Set.Suffix, []string{"googlevideo.com", "youtube.com", "ytimg.com"}) ||
		len(got.Set.Skipped) != 1 {
		t.Errorf("site on main: %+v %v", got, err)
	}
	got, err = r.Resolve(ctx, parse(t, "iplist:apple"))
	if err != nil || got.Portal != "beta" || got.Kind != "group" || !slices.Equal(got.Set.Suffix, []string{"apple.com", "icloud.com"}) {
		t.Errorf("group on beta: %+v %v", got, err)
	}

	before := up.Requests("/iplist/main/") + up.Requests("/iplist/russia/")
	got, err = r.Resolve(ctx, parse(t, "iplist:beta:apple"))
	if err != nil || got.Portal != "beta" || up.Requests("/iplist/main/")+up.Requests("/iplist/russia/") != before {
		t.Errorf("pinned asked other portals: %+v %v", got, err)
	}

	_, err = r.Resolve(ctx, parse(t, "iplist:example.org"))
	if !errors.Is(err, sources.ErrNotFound) || err.Error() != "sources: not found: not found on main, beta, russia" {
		t.Errorf("not found anywhere: %v", err)
	}

	up.PortalDown("beta", true)
	n := up.Requests("/iplist/beta/")
	_, err = r.Resolve(ctx, parse(t, "iplist:example.org"))
	var ue *sources.UnreachableError
	if !errors.As(err, &ue) || errors.Is(err, sources.ErrNotFound) || !slices.Equal(ue.Missed, []string{"main", "russia"}) ||
		!slices.Equal(ue.Unreachable, []string{"beta"}) || err.Error() != "not found on main, russia; beta unreachable" {
		t.Errorf("beta down: %v", err)
	}
	if c := up.Requests("/iplist/beta/") - n; c != 1 {
		t.Errorf("an unreachable portal was asked %d times", c)
	}
	if _, err := r.Resolve(ctx, parse(t, "iplist:beta:apple")); !errors.As(err, &ue) || err.Error() != "beta unreachable" {
		t.Errorf("pinned to a down portal: %v", err)
	}
}

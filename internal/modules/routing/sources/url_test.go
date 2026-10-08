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

func TestURLLists(t *testing.T) {
	ctx := context.Background()
	up := sourcestest.New(t)
	u := up.File("share/x/tunneled-domains.txt", "# mine\nexample.com\nfull:api.example.org\ninclude:more.txt @ads\n")
	up.File("share/x/more.txt", "more.com @ads\nnot-ads.com\n")
	r := resolver(up)
	got, err := r.Resolve(ctx, parse(t, u))
	if err != nil || !slices.Equal(got.Set.Suffix, []string{"example.com", "more.com"}) || !slices.Equal(got.Set.Exact, []string{"api.example.org"}) {
		t.Fatalf("%+v %v", got.Set, err)
	}
	if got, err := r.Resolve(ctx, parse(t, "mine="+u+"?v=2")); err != nil || got.Set.Count() != 3 {
		t.Errorf("named, with a query: %+v %v", got.Set, err)
	}

	_, err = r.Resolve(ctx, parse(t, up.URL()+"/files/missing.txt"))
	var he *sources.HTTPError
	if !errors.As(err, &he) || he.Status != 404 || !strings.HasSuffix(err.Error(), "/files/missing.txt: HTTP 404") {
		t.Errorf("404: %v", err)
	}

	big := up.File("big.txt", strings.Repeat("a.example.com\n", (8<<20)/14+1))
	if _, err := r.Resolve(ctx, parse(t, big)); !errors.Is(err, sources.ErrTooBig) {
		t.Errorf("over 8 MiB: %v", err)
	}
}

package sources_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/sources/sourcestest"
)

func resolver(up *sourcestest.Upstream) *sources.Resolver {
	return &sources.Resolver{Endpoints: up.Endpoints(), Fetch: &sources.Fetcher{Timeout: 5 * time.Second}}
}

func parse(t *testing.T, s string) selector.Selector {
	t.Helper()
	sel, err := selector.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

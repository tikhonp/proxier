package lists_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// ctx is an English request context.
func ctx(h *routingtest.Harness) context.Context {
	return i18n.WithLocalizer(context.Background(), h.App.I18n.Localizer(i18n.EN, time.UTC))
}

// order is a list's services' tags in order.
func order(t *testing.T, h *routingtest.Harness, id int64) string {
	t.Helper()
	v, err := h.Mod.Lists.View(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for i, m := range v.Members {
		if m.Position != i+1 {
			t.Errorf("%s at position %d, not %d", m.Service.Tag, m.Position, i+1)
		}
		tags = append(tags, m.Service.Tag)
	}
	return strings.Join(tags, ",")
}

// owner is the tag that installs the name in a list, "" for none.
func owner(t *testing.T, h *routingtest.Harness, id int64, name string, exact bool) string {
	t.Helper()
	v, err := h.Mod.Lists.View(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range v.Result.Services {
		names := o.Suffix
		if exact {
			names = o.Exact
		}
		for _, n := range names {
			if n == name {
				return o.Tag
			}
		}
	}
	return ""
}

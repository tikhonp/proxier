package pages

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const searchLimit = 20

// search answers the pop-up's "Go to" group: nav pages, settings sections
// and whatever the modules' Searchers find.
func (h *handler) search(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := web.FromContext(ctx).Loc
	q := strings.ToLower(strings.TrimSpace(c.QueryParam("q")))
	match := func(s string) bool { return q == "" || strings.Contains(strings.ToLower(s), q) }

	var hits []ui.SearchHit
	for _, g := range h.navGroups() {
		for _, it := range g.Items {
			if l := loc.T(it.Label); match(l) {
				hits = append(hits, ui.SearchHit{Label: l, Meta: loc.T(g.Label), Href: it.Href})
			}
		}
	}
	for _, p := range h.SettingsPages {
		if l := loc.T(p.Title); match(l) || match(loc.T("nav.settings")) {
			hits = append(hits, ui.SearchHit{Label: l, Meta: loc.T("nav.settings"), Href: "/settings/" + p.Slug})
		}
	}
	if q != "" {
		for _, s := range h.Searchers {
			found, err := s.Search(ctx, q, searchLimit)
			if err != nil {
				h.Log.Warn("search", "error", err)
				continue
			}
			hits = append(hits, found...)
		}
	}
	if len(hits) > searchLimit {
		hits = hits[:searchLimit]
	}
	return web.Render(c, http.StatusOK, searchHits(hits))
}

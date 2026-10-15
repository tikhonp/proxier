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
// and whatever the modules' Searchers find, of what the member sees. A
// module's hit that names no area is left out.
func (h *handler) search(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := web.FromContext(ctx).Loc
	g := web.FromContext(ctx).Grant
	q := strings.ToLower(strings.TrimSpace(c.QueryParam("q")))
	match := func(s string) bool { return q == "" || strings.Contains(strings.ToLower(s), q) }

	var hits []ui.SearchHit
	for _, ng := range h.navGroups(g) {
		for _, it := range ng.Items {
			if l := loc.T(it.Label); match(l) {
				hits = append(hits, ui.SearchHit{Label: l, Meta: loc.T(ng.Label), Href: it.Href})
			}
		}
	}
	for _, p := range h.SettingsPages {
		if !g.Sees(p.Area) {
			continue
		}
		if l := loc.T(p.Title); match(l) || match(loc.T("nav.settings")) {
			hits = append(hits, ui.SearchHit{Label: l, Meta: loc.T("nav.settings"), Href: "/settings/" + p.Slug})
		}
	}
	if q != "" {
		for _, s := range h.Searchers {
			found, err := s.Search(ctx, web.Who(ctx), q, searchLimit)
			if err != nil {
				h.Log.Warn("search", "error", err)
				continue
			}
			for _, hit := range found {
				if hit.Area.Valid() && g.Sees(hit.Area) {
					hits = append(hits, hit)
				}
			}
		}
	}
	if len(hits) > searchLimit {
		hits = hits[:searchLimit]
	}
	return web.Render(c, http.StatusOK, searchHits(hits))
}

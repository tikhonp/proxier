package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const searchPath = "/routing/search"

// previewNames is how many names the preview drawer shows.
const previewNames = 200

type resultRow struct {
	Selector, Line string
	Kind           string
	Lists          string // "Main", "" for not a service
	Selectable     bool
	PreviewHref    string
	AddHref        string // "" when nothing can be added
	AddLabel       string // i18n key
}

type searchView struct {
	Q, Source, Kind, Portal string
	Status                  string
	Failing                 []string
	Empty                   string // the catalog never filled
	Band                    string
	Chips                   []ui.Chip
	Kinds, Portals          []ui.FilterOption
	Rows                    []resultRow
	More                    string
	NoMatch                 string
	Discover, DiscoverHref  string // "kinopoisk.com": the empty result's hint
	Preview                 *previewView
}

type previewView struct {
	Selector, Line string
	Err            string
	Counts         []string
	Names          []diffLine
	More           string
	Skipped        []skipRow
	AddHref        string
	AddNote        string
	OpenHref       string
	CloseHref      string
}

func (h *handler) search(c *echo.Context) error {
	ctx := c.Request().Context()
	v, err := h.searchView(c)
	if err != nil {
		return err
	}
	if sel := c.QueryParam("selector"); sel != "" {
		p := h.previewOf(ctx, sel, v.closeHref())
		v.Preview = &p
	}
	return web.Render(c, http.StatusOK, searchPage(h.shell(c, i18n.T(ctx, "catalog.title"), searchPath), v))
}

func (v searchView) query(extra url.Values) url.Values {
	q := url.Values{}
	for k, val := range map[string]string{"q": v.Q, "source": v.Source, "kind": v.Kind, "portal": v.Portal} {
		if val != "" {
			q.Set(k, val)
		}
	}
	for k, vals := range extra {
		q[k] = vals
	}
	return q
}

func (v searchView) href(extra url.Values) string {
	q := v.query(extra)
	if len(q) == 0 {
		return searchPath
	}
	return searchPath + "?" + q.Encode()
}

func (v searchView) closeHref() string { return v.href(nil) }

func (h *handler) searchView(c *echo.Context) (searchView, error) {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	v := searchView{Q: strings.TrimSpace(c.QueryParam("q")), Source: c.QueryParam("source"), Kind: c.QueryParam("kind"), Portal: c.QueryParam("portal")}
	if v.Source != "v2fly" && v.Source != "iplist" {
		v.Source = ""
	}
	if !slices.Contains([]string{"list", "site", "group"}, v.Kind) {
		v.Kind = ""
	}
	if !slices.Contains([]string{"main", "beta", "russia"}, v.Portal) {
		v.Portal = ""
	}
	if err := h.catalogStatus(ctx, &v); err != nil {
		return v, err
	}
	if id, err := strconv.ParseInt(c.QueryParam("queued"), 10, 64); err == nil {
		v.Band = i18n.T(ctx, "catalog.queued_band", i18n.Args{"job": id})
	}
	v2, ip, err := h.Catalog.Matches(ctx, catalog.Query{Q: v.Q})
	if err != nil {
		return v, err
	}
	chip := func(label, source string, n int) ui.Chip {
		return ui.Chip{Label: label, Href: v.href(url.Values{"source": {source}}), On: v.Source == source}
	}
	all := chip(i18n.T(ctx, "catalog.chip.all"), "", 0)
	all.Href = searchView{Q: v.Q, Kind: v.Kind, Portal: v.Portal}.href(nil)
	v.Chips = []ui.Chip{all,
		chip(i18n.T(ctx, "catalog.chip.v2fly", i18n.Args{"n": v2}), "v2fly", v2),
		chip(i18n.T(ctx, "catalog.chip.iplist", i18n.Args{"n": ip}), "iplist", ip),
	}
	v.Kinds = []ui.FilterOption{{Value: "", Label: i18n.T(ctx, "catalog.kind.any")}}
	for _, k := range []string{"list", "group", "site"} {
		v.Kinds = append(v.Kinds, ui.FilterOption{Value: k, Label: i18n.T(ctx, "catalog.kind."+k)})
	}
	v.Portals = []ui.FilterOption{{Value: "", Label: i18n.T(ctx, "catalog.portal.any")}}
	for _, p := range []string{"main", "beta", "russia"} {
		v.Portals = append(v.Portals, ui.FilterOption{Value: p, Label: p})
	}
	if v.Q == "" {
		return v, nil
	}
	results, more, err := h.Catalog.Search(ctx, catalog.Query{Q: v.Q, Source: v.Source, Kind: v.Kind, Portal: v.Portal})
	if err != nil {
		return v, err
	}
	all2, err := h.Lists.All(ctx)
	if err != nil {
		return v, err
	}
	for _, r := range results {
		v.Rows = append(v.Rows, h.resultRow(ctx, v, r, len(all2)))
	}
	if more > 0 {
		v.More = loc.N("catalog.more", int64(more))
	}
	if len(results) == 0 {
		v.NoMatch = i18n.T(ctx, "catalog.no_match", i18n.Args{"q": v.Q})
		v.Discover = discoverGuess(v.Q)
		v.DiscoverHref = discoverPath + "?website=" + url.QueryEscape(v.Discover)
	}
	return v, nil
}

func (h *handler) resultRow(ctx context.Context, v searchView, r catalog.Result, lists int) resultRow {
	loc := i18n.From(ctx)
	row := resultRow{Selector: r.Selector, Kind: i18n.T(ctx, "catalog.kind."+r.Kind), Selectable: r.Selectable}
	var parts []string
	switch r.Kind {
	case "list":
		parts = append(parts, loc.N("catalog.line.list", int64(r.Domains)))
	case "group":
		parts = append(parts, loc.N("catalog.line.group", int64(r.Sites), i18n.Args{"domains": loc.Number(int64(r.Domains))}), r.Portal)
	case "site":
		parts = append(parts, i18n.T(ctx, "catalog.line.site", i18n.Args{"group": r.Group}), r.Portal)
	}
	if r.Portal != "" && r.Portal != "main" && strings.Count(r.Selector, ":") == 2 {
		parts = append(parts, i18n.T(ctx, "catalog.line.pinned", i18n.Args{"portal": r.Portal}))
	}
	if !r.Selectable {
		parts = append(parts, i18n.T(ctx, "catalog.line.shadowed"))
	}
	if a := ageWord(ctx, r.Age); a != "" {
		parts = append(parts, a)
	}
	row.Line = strings.Join(parts, " · ")
	row.PreviewHref = v.href(url.Values{"selector": {r.Selector}})
	switch {
	case !r.Selectable:
	case r.Service == nil:
		row.AddHref, row.AddLabel = listPath+"/add?selector="+url.QueryEscape(r.Selector), "catalog.add"
	case len(r.Lists) < lists:
		row.AddHref = svcHref(r.Service.ID) + "/lists"
		row.AddLabel = "catalog.add_more"
		row.Lists = strings.Join(r.Lists, ", ")
	default:
		row.AddHref, row.AddLabel = svcHref(r.Service.ID), "catalog.open"
		row.Lists = strings.Join(r.Lists, ", ")
	}
	if r.Service != nil && row.Lists == "" {
		row.Lists = strings.Join(r.Lists, ", ")
	}
	return row
}

// catalogStatus words the status line: what the catalog is, when it was
// refreshed, the failing sources and an empty catalog.
func (h *handler) catalogStatus(ctx context.Context, v *searchView) error {
	loc := i18n.From(ctx)
	st, err := h.Catalog.Status(ctx)
	if err != nil {
		return err
	}
	var newest time.Time
	for _, s := range st {
		if s.RefreshedAt.After(newest) {
			newest = s.RefreshedAt
		}
		if s.Failures > 0 {
			args := i18n.Args{"source": s.Source, "since": loc.ShortDate(s.FailingSince), "date": "—"}
			if !s.RefreshedAt.IsZero() {
				args["date"] = loc.ShortDate(s.RefreshedAt)
			}
			v.Failing = append(v.Failing, i18n.T(ctx, "catalog.status.failing", args))
		}
	}
	at, _ := h.Settings.Get(ctx, "routing.catalog_at")
	v.Status = i18n.T(ctx, "catalog.status")
	if newest.IsZero() {
		v.Empty = i18n.T(ctx, "catalog.empty", i18n.Args{"at": at})
		return nil
	}
	v.Status += " · " + i18n.T(ctx, "catalog.status.refreshed", i18n.Args{"time": loc.Time(newest)})
	return nil
}

func (h *handler) preview(c *echo.Context) error {
	ctx := c.Request().Context()
	sel := strings.TrimSpace(c.QueryParam("selector"))
	if !htmx(c) {
		q := c.QueryParams()
		return web.Redirect(c, searchPath+"?"+q.Encode())
	}
	if sel == "" {
		return c.HTML(http.StatusOK, "")
	}
	p := h.previewOf(ctx, sel, "")
	return web.Render(c, http.StatusOK, previewDrawer(p))
}

// previewOf resolves a selector now, within the interactive deadline, and
// words what it holds.
func (h *handler) previewOf(ctx context.Context, sel, closeHref string) previewView {
	loc := i18n.From(ctx)
	v := previewView{Selector: sel, CloseHref: closeHref}
	p, err := h.Services.Preview(ctx, sel)
	var ee *services.EmptyResolveError
	if err != nil && !errors.As(err, &ee) {
		msg, ok := selectorError(ctx, sel, err)
		if !ok {
			msg = i18n.T(ctx, "services.err.fetch", i18n.Args{"error": err.Error()})
		}
		v.Err = msg
		return v
	}
	if err == nil {
		v.Selector = p.Stored()
	}
	set := p.Resolved.Set
	if it, serr := h.Services.BySelector(ctx, v.Selector); serr == nil {
		names, _ := h.Services.ListsOf(ctx, it.ID)
		v.Line = i18n.T(ctx, "catalog.preview.service", i18n.Args{"tag": it.Tag, "lists": inLists(ctx, names)})
		v.OpenHref = svcHref(it.ID)
	} else {
		v.Line = i18n.T(ctx, "catalog.preview.new")
		if err == nil {
			v.AddHref = listPath + "/add?selector=" + url.QueryEscape(v.Selector)
			v.AddNote = i18n.T(ctx, "catalog.preview.creates", i18n.Args{"tag": p.Tag})
		}
	}
	v.Counts = []string{
		i18n.T(ctx, "catalog.preview.suffix", i18n.Args{"n": loc.Number(int64(len(set.Suffix)))}),
		i18n.T(ctx, "catalog.preview.exact", i18n.Args{"n": loc.Number(int64(len(set.Exact)))}),
		loc.N("services.skipped_n", int64(len(set.Skipped))),
	}
	var all []diffLine
	for _, n := range set.Suffix {
		all = append(all, diffLine{Name: n, Unicode: domain.Unicode(n), Kind: i18n.T(ctx, "services.kind.suffix")})
	}
	for _, n := range set.Exact {
		all = append(all, diffLine{Name: n, Unicode: domain.Unicode(n), Kind: i18n.T(ctx, "services.kind.exact")})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	if len(all) > previewNames {
		v.More = i18n.T(ctx, "services.diff.more", i18n.Args{"n": len(all) - previewNames})
		all = all[:previewNames]
	}
	v.Names = all
	for _, s := range set.Skipped {
		v.Skipped = append(v.Skipped, skipRow{Entry: s.Entry, Reason: i18n.T(ctx, s.Reason)})
	}
	if ee != nil {
		v.Err, _ = selectorError(ctx, sel, err)
	}
	return v
}

func (h *handler) catalogRefresh(c *echo.Context) error {
	id, err := h.Catalog.RefreshNow(c.Request().Context(), events.ActorAdmin)
	if err != nil {
		return err
	}
	q := url.Values{"queued": {strconv.FormatInt(id, 10)}}
	if s := strings.TrimSpace(c.FormValue("q")); s != "" {
		q.Set("q", s)
	}
	return web.Redirect(c, searchPath+"?"+q.Encode())
}

// previewURL is the drawer's address for a selector.
func previewURL(sel string) string { return searchPath + "/preview?selector=" + url.QueryEscape(sel) }

// discoverGuess is the website an empty search suggests discovering: a
// dotless query gets .com, a query with a dot is used as typed.
func discoverGuess(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	if !strings.Contains(q, ".") {
		return q + ".com"
	}
	return q
}

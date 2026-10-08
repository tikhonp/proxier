package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ---------------------------------------------------------------- the list

type svcRow struct {
	ID               int64
	Tag, Sub, Source string
	Domains, When    string
	StateKind, State string
	Lists            string
	NoLists          bool
}

type listView struct {
	Status   string
	Chips    []ui.Chip
	Rows     []svcRow
	Empty    bool // no services at all
	Info     string
	NextHref string
	PrevHref string
}

var sourceFilters = []string{"", "v2fly", "iplist", "url", "custom"}

func (h *handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	src := c.QueryParam("source")
	if !contains(sourceFilters, src) {
		src = ""
	}
	all, custom, err := h.Services.Counts(ctx)
	if err != nil {
		return err
	}
	f := services.Filter{Source: src}
	listParam := c.QueryParam("list")
	if listParam == "none" {
		f.NoList = true
	} else if n, err := strconv.ParseInt(listParam, 10, 64); err == nil && n > 0 {
		f.List = n
	} else {
		listParam = ""
	}
	rows, err := h.Services.List(ctx, f)
	if err != nil {
		return err
	}
	v := listView{
		Empty:  all == 0,
		Status: loc.N("services.status.services", int64(all)) + " · " + loc.N("services.status.custom", int64(custom)),
	}
	query := func(source, list string) string {
		q := url.Values{}
		if source != "" {
			q.Set("source", source)
		}
		if list != "" {
			q.Set("list", list)
		}
		if len(q) == 0 {
			return listPath
		}
		return listPath + "?" + q.Encode()
	}
	for _, s := range sourceFilters {
		label := i18n.T(ctx, "services.filter.all")
		if s != "" {
			label = s
		}
		v.Chips = append(v.Chips, ui.Chip{Label: i18n.T(ctx, "services.filter.source", i18n.Args{"source": label}), Href: query(s, listParam), On: s == src})
	}
	// List chips: pressing the pressed one clears it.
	rls, err := h.Lists.All(ctx)
	if err != nil {
		return err
	}
	for _, l := range rls {
		id := i64(l.ID)
		href := query(src, id)
		if listParam == id {
			href = query(src, "")
		}
		v.Chips = append(v.Chips, ui.Chip{Label: i18n.T(ctx, "lists.filter.list", i18n.Args{"name": l.Name}), Href: href, On: listParam == id})
	}
	free, err := h.Services.List(ctx, services.Filter{NoList: true})
	if err != nil {
		return err
	}
	href := query(src, "none")
	if listParam == "none" {
		href = query(src, "")
	}
	v.Chips = append(v.Chips, ui.Chip{Label: i18n.T(ctx, "lists.filter.none", i18n.Args{"n": len(free)}), Href: href, On: listParam == "none"})
	p := pageOf(c)
	from, to := window(len(rows), p)
	for _, r := range rows[from:to] {
		sr := svcRow{
			ID: r.ID, Tag: r.Tag, Sub: sourceWord(ctx, r.Item), Source: string(r.Source),
			Domains:   i18n.T(ctx, "services.col.domains_value", i18n.Args{"suffix": loc.Number(int64(r.Suffix)), "exact": loc.Number(int64(r.Exact))}),
			StateKind: "ok", State: i18n.T(ctx, "services.state."+r.State),
			Lists: strings.Join(r.Lists, ", "), NoLists: len(r.Lists) == 0,
		}
		if r.Source == selector.Custom {
			sr.When = i18n.T(ctx, "services.saved_on", i18n.Args{"date": loc.ShortDate(r.SavedAt)})
		} else {
			sr.When = i18n.T(ctx, "services.added_on", i18n.Args{"date": loc.ShortDate(r.CreatedAt)})
		}
		v.Rows = append(v.Rows, sr)
	}
	if len(rows) > 0 {
		v.Info = i18n.T(ctx, "services.pager", i18n.Args{"from": from + 1, "to": to, "n": len(rows)})
	}
	page := func(n int) string {
		q := url.Values{"page": {strconv.Itoa(n)}}
		if src != "" {
			q.Set("source", src)
		}
		if listParam != "" {
			q.Set("list", listParam)
		}
		return listPath + "?" + q.Encode()
	}
	if to < len(rows) {
		v.NextHref = page(p + 1)
	}
	if p > 1 && from > 0 {
		v.PrevHref = page(p - 1)
	}
	return web.Render(c, http.StatusOK, listPage(h.shell(c, i18n.T(ctx, "services.title"), listPath), v))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------- add

type takenView struct {
	ID                    int64
	Tag, Selector, Source string
	Lists                 string
	Same                  bool   // the same selector: only Open
	New                   string // the selector to switch to
	Suggest               string // for a URL: "<tag>-2=<url>"
}

type listCheck struct {
	ID      int64
	Name    string
	Checked bool
}

type addView struct {
	Selector string
	Err      string
	Taken    *takenView
	Lists    []listCheck // Add to routing lists
	FromList bool        // posted from a list's Add services page
}

// listChecks are the lists to tick: the default one on a first visit.
func (h *handler) listChecks(ctx context.Context, checked []int64, first bool) ([]listCheck, error) {
	all, err := h.Lists.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]listCheck, 0, len(all))
	for _, l := range all {
		out = append(out, listCheck{ID: l.ID, Name: l.Name, Checked: first && l.Default || slices.Contains(checked, l.ID)})
	}
	return out, nil
}

func (h *handler) addPage(c *echo.Context) error {
	ls, err := h.listChecks(c.Request().Context(), nil, true)
	if err != nil {
		return err
	}
	return h.renderAdd(c, http.StatusOK, addView{Selector: c.QueryParam("selector"), Lists: ls})
}

func (h *handler) renderAdd(c *echo.Context, status int, v addView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, addPage(h.shell(c, i18n.T(ctx, "services.add.title"), listPath), v))
}

// add adds an upstream service, and with lists ticked puts it into them in
// the same step (the guard checked first).
func (h *handler) add(c *echo.Context) error {
	ctx := c.Request().Context()
	raw := strings.TrimSpace(c.FormValue("selector"))
	listIDs := formIDs(c, "lists")
	fromList := c.FormValue("from_list") == "1"
	var id int64
	var err error
	if len(listIDs) > 0 {
		id, err = h.Lists.AddNew(ctx, raw, listIDs, events.ActorAdmin)
	} else {
		id, err = h.Services.Add(ctx, raw, events.ActorAdmin)
	}
	if err == nil {
		if fromList && len(listIDs) == 1 {
			return web.Redirect(c, listHref(listIDs[0])+"?added=1")
		}
		return web.Redirect(c, svcHref(id)+"?added=1")
	}
	ls, lerr := h.listChecks(ctx, listIDs, false)
	if lerr != nil {
		return lerr
	}
	v := addView{Selector: raw, Lists: ls, FromList: fromList}
	var taken *services.TagTakenError
	var ge *lists.GuardError
	switch {
	case errors.As(err, &taken):
		if v.Taken, err = h.takenView(c, raw, taken); err != nil {
			return err
		}
		return h.renderAdd(c, http.StatusOK, v)
	case errors.As(err, &ge):
		v.Err = guardText(ctx, ge)
		return h.renderAdd(c, http.StatusUnprocessableEntity, v)
	case errors.Is(err, lists.ErrNotFound):
		v.Err = i18n.T(ctx, "lists.err.gone")
		return h.renderAdd(c, http.StatusUnprocessableEntity, v)
	}
	msg, ok := selectorError(ctx, raw, err)
	if !ok {
		return err
	}
	v.Err = msg
	return h.renderAdd(c, http.StatusUnprocessableEntity, v)
}

// takenView shows the service that has the tag, and what can be done.
func (h *handler) takenView(c *echo.Context, raw string, e *services.TagTakenError) (*takenView, error) {
	ctx := c.Request().Context()
	ex := e.Existing
	lists, err := h.Services.ListsOf(ctx, ex.ID)
	if err != nil {
		return nil, err
	}
	sel, _ := selector.Parse(raw)
	tv := &takenView{
		ID: ex.ID, Tag: ex.Tag, Selector: sourceWord(ctx, ex), Source: string(ex.Source), Lists: inLists(ctx, lists),
		Same: sel.String() == ex.Selector, New: sel.String(),
	}
	if ex.Source == selector.Custom {
		tv.Same = true // a custom service has no source to switch
	}
	if sel.Source == selector.URL {
		for n := 2; n < 100; n++ {
			tag := e.Tag + "-" + strconv.Itoa(n)
			if _, err := h.Services.ByTag(ctx, tag); errors.Is(err, services.ErrNotFound) {
				if selector.ValidTag(tag) == nil {
					tv.Suggest = tag + "=" + sel.URL
				}
				break
			} else if err != nil {
				return nil, err
			}
		}
	}
	return tv, nil
}

// ---------------------------------------------------------- custom create

type newView struct {
	Name, Tag, Description string
	Errs                   map[string]string
}

func (h *handler) newPage(c *echo.Context) error {
	return h.renderNew(c, http.StatusOK, newView{})
}

func (h *handler) renderNew(c *echo.Context, status int, v newView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, newPage(h.shell(c, i18n.T(ctx, "services.new.title"), listPath), v))
}

func (h *handler) create(c *echo.Context) error {
	ctx := c.Request().Context()
	v := newView{Name: c.FormValue("name"), Tag: c.FormValue("tag"), Description: c.FormValue("description")}
	id, err := h.Services.CreateCustom(ctx, services.Custom{Name: v.Name, Tag: v.Tag, Description: v.Description}, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderNew(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, svcHref(id)+"/edit")
}

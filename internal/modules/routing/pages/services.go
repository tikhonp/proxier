package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
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
	rows, err := h.Services.List(ctx, services.Filter{Source: src})
	if err != nil {
		return err
	}
	v := listView{
		Empty:  all == 0,
		Status: loc.N("services.status.services", int64(all)) + " · " + loc.N("services.status.custom", int64(custom)),
	}
	for _, s := range sourceFilters {
		href := listPath
		label := i18n.T(ctx, "services.filter.all")
		if s != "" {
			href += "?source=" + s
			label = s
		}
		v.Chips = append(v.Chips, ui.Chip{Label: i18n.T(ctx, "services.filter.source", i18n.Args{"source": label}), Href: href, On: s == src})
	}
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

type addView struct {
	Selector string
	Err      string
	Taken    *takenView
}

func (h *handler) addPage(c *echo.Context) error {
	return h.renderAdd(c, http.StatusOK, addView{Selector: c.QueryParam("selector")})
}

func (h *handler) renderAdd(c *echo.Context, status int, v addView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, addPage(h.shell(c, i18n.T(ctx, "services.add.title"), listPath), v))
}

func (h *handler) add(c *echo.Context) error {
	ctx := c.Request().Context()
	raw := strings.TrimSpace(c.FormValue("selector"))
	id, err := h.Services.Add(ctx, raw, events.ActorAdmin)
	var taken *services.TagTakenError
	switch {
	case err == nil:
		return web.Redirect(c, svcHref(id)+"?added=1")
	case errors.As(err, &taken):
		tv, err := h.takenView(c, raw, taken)
		if err != nil {
			return err
		}
		return h.renderAdd(c, http.StatusOK, addView{Selector: raw, Taken: tv})
	}
	msg, ok := selectorError(ctx, raw, err)
	if !ok {
		return err
	}
	return h.renderAdd(c, http.StatusUnprocessableEntity, addView{Selector: raw, Err: msg})
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

package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const listsPath = "/routing/lists"

func listHref(id int64) string { return listsPath + "/" + i64(id) }

// registerLists adds the routing lists' routes.
func (h *handler) registerLists(r web.Routes) {
	r.Admin.GET(listsPath, h.lists)
	r.Admin.GET(listsPath+"/new", h.newListPage)
	r.Admin.POST(listsPath, h.createList)
	r.Admin.GET(listsPath+"/:id", h.listPage)
	r.Admin.GET(listsPath+"/:id/edit", h.editListPage)
	r.Admin.POST(listsPath+"/:id/edit", h.editList)
	r.Admin.POST(listsPath+"/:id/default", h.makeDefault)
	r.Admin.GET(listsPath+"/:id/add", h.addToListPage)
	r.Admin.POST(listsPath+"/:id/services", h.addToList)
	r.Admin.POST(listsPath+"/:id/services/order", h.orderList)
	r.Admin.POST(listsPath+"/:id/services/:service/remove", h.removeFromList)
	r.Admin.POST(listsPath+"/:id/services/:service/move", h.moveInList)
	r.Admin.GET(listsPath+"/:id/delete", h.deleteListPage)
	r.Admin.POST(listsPath+"/:id/delete", h.deleteList)
	r.Admin.GET(listPath+"/:id/lists", h.serviceListsPage)
	r.Admin.POST(listPath+"/:id/lists", h.serviceLists)
}

// loadList reads the list of the :id parameter; 404 when there is none.
func (h *handler) loadList(c *echo.Context) (lists.List, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return lists.List{}, echo.ErrNotFound
	}
	l, err := h.Lists.Get(c.Request().Context(), id)
	if errors.Is(err, lists.ErrNotFound) {
		return l, echo.ErrNotFound
	}
	return l, err
}

// targetView is a target with its status marker.
type targetView struct {
	Name, Kind, State, Word, Href, Note string
}

// targetsOf words the targets: routers with their sync word ("synced 1 h
// ago", "syncing", "sync failed · connect", "waits 30 s"), configs with
// their last fetch.
func (h *handler) targetsOf(ctx context.Context, ts []lists.Target) ([]targetView, error) {
	out := make([]targetView, 0, len(ts))
	for _, t := range ts {
		kind := "ok"
		if t.State != "active" && t.State != "enabled" {
			kind = "off"
		}
		tv := targetView{Name: t.Name, Kind: kind, State: t.State, Word: i18n.T(ctx, "lists.target."+t.Kind) + " · " + t.State}
		if t.Kind == "router" && h.Routers != nil {
			r, err := h.Routers.Get(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			k, w, err := h.routerWord(ctx, r)
			if err != nil {
				return nil, err
			}
			tv.Kind, tv.Word, tv.Href = k, i18n.T(ctx, "lists.target.router")+" · "+w, routerHref(t.ID)
		}
		if t.Kind == "shadowrocket" {
			tv.Href = srHref(t.ID)
			switch {
			case t.State == "disabled":
				tv.Word = i18n.T(ctx, "lists.target.shadowrocket") + " · " + i18n.T(ctx, "shadowrocket.disabled")
			case t.LastFetch.IsZero():
				tv.Word = i18n.T(ctx, "lists.target.shadowrocket") + " · " + i18n.T(ctx, "shadowrocket.never_fetched")
				tv.Note = i18n.T(ctx, "shadowrocket.target.note")
			default:
				tv.Word = i18n.T(ctx, "lists.target.shadowrocket") + " · " + i18n.T(ctx, "shadowrocket.fetched", i18n.Args{"ago": i18n.From(ctx).Ago(t.LastFetch)})
				tv.Note = i18n.T(ctx, "shadowrocket.target.note")
			}
		}
		out = append(out, tv)
	}
	return out, nil
}

// ------------------------------------------------------------------ the lists page

type listsRow struct {
	ID                int64
	Name, Sub         string
	Default           bool
	Services, Domains string
	Targets           []targetView
	Warn              string
}

type listsView struct {
	Rows []listsRow
	Line string
}

func (h *handler) lists(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	rows, err := h.Lists.List(ctx)
	if err != nil {
		return err
	}
	v := listsView{Line: loc.N("lists.status", int64(len(rows)))}
	for _, r := range rows {
		ts, err := h.targetsOf(ctx, r.Targets)
		if err != nil {
			return err
		}
		lr := listsRow{
			ID: r.ID, Name: r.Name, Sub: r.Description, Default: r.Default,
			Services: loc.Number(int64(r.Services)), Domains: loc.Number(int64(r.Domains)), Targets: ts,
		}
		if r.Default {
			lr.Sub = i18n.T(ctx, "lists.default_line")
		}
		if r.Warnings > 0 {
			lr.Warn = loc.N("lists.warnings_n", int64(r.Warnings))
		}
		v.Rows = append(v.Rows, lr)
	}
	return web.Render(c, http.StatusOK, listsPage(h.shell(c, i18n.T(ctx, "lists.title"), listsPath), v))
}

// ------------------------------------------------------------------ new, edit

type listFormView struct {
	L                 lists.List // zero for a new one
	Name, Description string
	Errs              map[string]string
}

func (h *handler) newListPage(c *echo.Context) error {
	return h.renderListForm(c, http.StatusOK, listFormView{})
}

func (h *handler) renderListForm(c *echo.Context, status int, v listFormView) error {
	ctx := c.Request().Context()
	title := i18n.T(ctx, "lists.new.title")
	if v.L.ID != 0 {
		title = i18n.T(ctx, "lists.edit.title", i18n.Args{"name": v.L.Name})
	}
	return web.Render(c, status, listFormPage(h.shell(c, title, listsPath), v, title))
}

func (h *handler) createList(c *echo.Context) error {
	ctx := c.Request().Context()
	v := listFormView{Name: c.FormValue("name"), Description: c.FormValue("description")}
	id, err := h.Lists.Create(ctx, v.Name, v.Description, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderListForm(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, listHref(id))
}

func (h *handler) editListPage(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	return h.renderListForm(c, http.StatusOK, listFormView{L: l, Name: l.Name, Description: l.Description})
}

func (h *handler) editList(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := listFormView{L: l, Name: c.FormValue("name"), Description: c.FormValue("description")}
	_, err = h.Lists.Edit(ctx, l.ID, v.Name, v.Description, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderListForm(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, listHref(l.ID))
}

func (h *handler) makeDefault(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	if err := h.Lists.MakeDefault(c.Request().Context(), l.ID, events.ActorAdmin); err != nil {
		return err
	}
	return web.Redirect(c, listHref(l.ID))
}

// ------------------------------------------------------------------ add services

type pickRow struct {
	ID                             int64
	Tag, Selector, Source, Domains string
	Checked                        bool
}

type addToListView struct {
	L       lists.List
	Q       string
	Rows    []pickRow
	Err     string
	Checked map[int64]bool
}

func (h *handler) addToListView(ctx context.Context, l lists.List, q string, checked map[int64]bool) (addToListView, error) {
	v := addToListView{L: l, Q: strings.TrimSpace(q)}
	rows, err := h.Services.List(ctx, servicesFilterAll)
	if err != nil {
		return v, err
	}
	in, err := store.ServicesInList(ctx, h.DB.R, l.ID)
	if err != nil {
		return v, err
	}
	lq := strings.ToLower(v.Q)
	loc := i18n.From(ctx)
	for _, r := range rows {
		if in[r.ID] {
			continue
		}
		if lq != "" && !strings.Contains(r.Tag, lq) && !strings.Contains(strings.ToLower(r.Selector), lq) && !strings.Contains(strings.ToLower(r.Name), lq) {
			continue
		}
		v.Rows = append(v.Rows, pickRow{
			ID: r.ID, Tag: r.Tag, Selector: sourceWord(ctx, r.Item), Source: string(r.Source),
			Domains: loc.Number(int64(r.Count)), Checked: checked[r.ID],
		})
	}
	return v, nil
}

func (h *handler) addToListPage(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	v, err := h.addToListView(c.Request().Context(), l, c.QueryParam("q"), nil)
	if err != nil {
		return err
	}
	return h.renderAddToList(c, http.StatusOK, v)
}

func (h *handler) renderAddToList(c *echo.Context, status int, v addToListView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, addToListPage(h.shell(c, i18n.T(ctx, "lists.add.title", i18n.Args{"name": v.L.Name}), listsPath), v))
}

// ids reads repeated id fields.
func formIDs(c *echo.Context, name string) []int64 {
	var out []int64
	if err := c.Request().ParseForm(); err != nil {
		return nil
	}
	for _, s := range c.Request().PostForm[name] {
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// guardText words a guard refusal.
func guardText(ctx context.Context, ge *lists.GuardError) string {
	return i18n.T(ctx, "lists.err.guard", i18n.Args{"domain": ge.Domain, "hostname": ge.Hostname, "server": ge.Server, "list": ge.List, "service": ge.Service})
}

func (h *handler) addToList(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	ids := formIDs(c, "service")
	err = h.Lists.Add(ctx, l.ID, ids, events.ActorAdmin)
	var ge *lists.GuardError
	if errors.As(err, &ge) {
		checked := map[int64]bool{}
		for _, id := range ids {
			checked[id] = true
		}
		v, verr := h.addToListView(ctx, l, "", checked)
		if verr != nil {
			return verr
		}
		v.Err = guardText(ctx, ge)
		return h.renderAddToList(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, listHref(l.ID)+"?added="+strconv.Itoa(len(ids)))
}

// ------------------------------------------------------------------ delete

type deleteListView struct {
	L       lists.List
	Targets []targetView
	Others  []lists.List
	MoveTo  int64
	Size    int
	Err     string
}

func (h *handler) deleteListView(ctx context.Context, l lists.List) (deleteListView, error) {
	v := deleteListView{L: l}
	ts, err := h.Lists.Targets(ctx, l.ID)
	if err != nil {
		return v, err
	}
	if v.Targets, err = h.targetsOf(ctx, ts); err != nil {
		return v, err
	}
	all, err := h.Lists.All(ctx)
	if err != nil {
		return v, err
	}
	for _, o := range all {
		if o.ID != l.ID {
			v.Others = append(v.Others, o)
			if o.Default {
				v.MoveTo = o.ID
			}
		}
	}
	v.Size, err = h.Lists.Size(ctx, l.ID)
	return v, err
}

func (h *handler) deleteListPage(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	v, err := h.deleteListView(c.Request().Context(), l)
	if err != nil {
		return err
	}
	return h.renderDeleteList(c, http.StatusOK, v)
}

func (h *handler) renderDeleteList(c *echo.Context, status int, v deleteListView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, deleteListPage(h.shell(c, i18n.T(ctx, "lists.delete.title", i18n.Args{"name": v.L.Name}), listsPath), v))
}

func (h *handler) deleteList(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	moveTo, _ := strconv.ParseInt(c.FormValue("move_to"), 10, 64)
	err = h.Lists.Delete(ctx, l.ID, moveTo, events.ActorAdmin)
	var he *lists.HasTargetsError
	switch {
	case err == nil:
		return web.Redirect(c, listsPath)
	case errors.Is(err, lists.ErrDefault), errors.As(err, &he), errors.Is(err, lists.ErrMoveTo):
		v, verr := h.deleteListView(ctx, l)
		if verr != nil {
			return verr
		}
		switch {
		case errors.Is(err, lists.ErrDefault):
			v.Err = i18n.T(ctx, "lists.delete.default")
		case he != nil:
			v.Err = i18n.T(ctx, "lists.delete.targets_meanwhile")
		default:
			v.Err = i18n.T(ctx, "lists.delete.move_to")
		}
		return h.renderDeleteList(c, http.StatusConflict, v)
	}
	return err
}

// ------------------------------------------------------------------ a service's lists

type serviceListsView struct {
	ID    int64
	Tag   string
	Lists []lists.List // it isn't in yet
	In    string       // "in Main and Parents"
	Err   string
}

func (h *handler) serviceListsView(ctx context.Context, id int64, tag string) (serviceListsView, error) {
	v := serviceListsView{ID: id, Tag: tag}
	all, err := h.Lists.All(ctx)
	if err != nil {
		return v, err
	}
	ms, err := h.Lists.Memberships(ctx, id)
	if err != nil {
		return v, err
	}
	in := map[int64]bool{}
	var names []string
	for _, m := range ms {
		in[m.List.ID] = true
		names = append(names, m.List.Name)
	}
	v.In = inLists(ctx, names)
	for _, l := range all {
		if !in[l.ID] {
			v.Lists = append(v.Lists, l)
		}
	}
	return v, nil
}

func (h *handler) serviceListsPage(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	v, err := h.serviceListsView(c.Request().Context(), it.ID, it.Tag)
	if err != nil {
		return err
	}
	return h.renderServiceLists(c, http.StatusOK, v)
}

func (h *handler) renderServiceLists(c *echo.Context, status int, v serviceListsView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, serviceListsPage(h.shell(c, i18n.T(ctx, "lists.to_lists.title", i18n.Args{"tag": v.Tag}), listPath), v))
}

func (h *handler) serviceLists(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	for _, l := range formIDs(c, "list") {
		err := h.Lists.Add(ctx, l, []int64{it.ID}, events.ActorAdmin)
		var ge *lists.GuardError
		if errors.As(err, &ge) {
			v, verr := h.serviceListsView(ctx, it.ID, it.Tag)
			if verr != nil {
				return verr
			}
			v.Err = guardText(ctx, ge)
			return h.renderServiceLists(c, http.StatusUnprocessableEntity, v)
		}
		if errors.Is(err, lists.ErrNotFound) {
			continue // deleted meanwhile
		}
		if err != nil {
			return err
		}
	}
	return web.Redirect(c, svcHref(it.ID))
}

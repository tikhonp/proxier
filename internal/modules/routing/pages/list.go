package pages

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

var servicesFilterAll = services.Filter{}

type memberRow struct {
	ID           int64
	Pos          int
	Tag, Sub     string
	Owned, Total string
	Same         bool // owned = total: the total is muted
	Source       string
	First, Last  bool
	Cursor       bool
}

// undoView is the band after a reorder.
type undoView struct {
	Moved, Owners, Targets string
	Order, Expect          string // the order to go back to, and the order now
}

type membersView struct {
	ListID   int64
	Name     string
	Rows     []memberRow
	OrderCSV string
	Stale    bool
	Undo     *undoView
}

type warnView struct {
	Time, Text, Href string
}

type listPageView struct {
	L        lists.List
	Line     string
	Band     string
	Members  membersView
	Targets  []targetView
	Warnings []warnView
	Activity []eventLine
}

func csv(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, i64(id))
	}
	return strings.Join(parts, ",")
}

func parseCSV(s string) ([]int64, bool) {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// membersView is the Services area of a view, the cursor on moved (0: none).
func membersOf(ctx context.Context, v lists.View, moved int64) membersView {
	loc := i18n.From(ctx)
	mv := membersView{ListID: v.List.ID, Name: v.List.Name}
	var ids []int64
	for i, m := range v.Members {
		r := memberRow{
			ID: m.Service.ID, Pos: m.Position, Tag: m.Service.Tag, Source: string(m.Service.Source),
			Owned: loc.Number(int64(m.Owned.Count())), Total: loc.Number(int64(m.Owned.Total)), Same: m.Owned.Count() == m.Owned.Total,
			First: i == 0, Last: i == len(v.Members)-1, Cursor: m.Service.ID == moved,
		}
		if m.Service.Source != selector.Custom {
			r.Sub = m.Service.Selector
		}
		mv.Rows = append(mv.Rows, r)
		ids = append(ids, m.Service.ID)
	}
	mv.OrderCSV = csv(ids)
	return mv
}

func (h *handler) listPage(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	view, err := h.Lists.View(ctx, l.ID, nil)
	if err != nil {
		return err
	}
	v := listPageView{L: l, Members: membersOf(ctx, view, 0)}
	v.Line = loc.N("lists.services_n", int64(len(view.Members))) + " · " + loc.N("lists.domains_n", int64(view.Result.Count()))
	if l.Default {
		v.Line += " · " + i18n.T(ctx, "lists.default_line")
	}
	if n, err := strconv.Atoi(c.QueryParam("added")); err == nil && n > 0 {
		v.Band = loc.N("lists.added_band", int64(n))
	}
	ts, err := h.Lists.Targets(ctx, l.ID)
	if err != nil {
		return err
	}
	v.Targets = targetsOf(ctx, ts)
	ws, err := h.Lists.Warnings(ctx, l.ID)
	if err != nil {
		return err
	}
	for _, w := range ws {
		wv := warnView{Text: w.Text, Href: w.Href}
		if !w.At.IsZero() {
			wv.Time = loc.Ago(w.At)
		}
		v.Warnings = append(v.Warnings, wv)
	}
	if v.Activity, err = h.listActivity(ctx, l); err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, rlistPage(h.shell(c, l.Name, listsPath), v))
}

func (h *handler) listActivity(ctx context.Context, l lists.List) ([]eventLine, error) {
	loc := i18n.From(ctx)
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: lists.Subject(l.ID), Limit: 10})
	if err != nil {
		return nil, err
	}
	var out []eventLine
	for _, e := range list {
		args := i18n.Args{"subject": l.Name, "actor": e.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		key := "event." + e.Type
		if e.Type == "routing.list_updated" {
			key = "lists.event." + listChange(e.Payload)
		}
		text := e.Type
		if loc.Has(key) {
			text = loc.T(key, args)
		}
		out = append(out, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return out, nil
}

// listChange picks the wording of a list_updated line.
func listChange(p map[string]any) string {
	switch {
	case p["added"] != nil:
		return "added"
	case p["removed"] != nil:
		return "removed"
	case p["reordered"] == true:
		return "reordered"
	case p["changes"] == "default":
		return "default"
	}
	return "fields"
}

// answerMembers re-renders the Services area for htmx and redirects to the
// page otherwise; htmx swaps only 2xx answers, so a stale order answers 200
// with the order as stored and says why (2a's pattern).
func (h *handler) answerMembers(c *echo.Context, l lists.List, moved int64, stale bool, undo *undoView) error {
	if !htmx(c) {
		if stale {
			return echo.NewHTTPError(http.StatusConflict, "the services changed meanwhile; reload the page")
		}
		return web.Redirect(c, listHref(l.ID))
	}
	ctx := c.Request().Context()
	view, err := h.Lists.View(ctx, l.ID, nil)
	if err != nil {
		return err
	}
	mv := membersOf(ctx, view, moved)
	mv.Stale, mv.Undo = stale, undo
	return web.Render(c, http.StatusOK, membersArea(mv))
}

func serviceParam(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("service"), 10, 64)
	if err != nil {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

func (h *handler) removeFromList(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	sid, err := serviceParam(c)
	if err != nil {
		return err
	}
	err = h.Lists.Remove(c.Request().Context(), l.ID, sid, events.ActorAdmin)
	if errors.Is(err, lists.ErrNotMember) {
		return h.answerMembers(c, l, 0, true, nil)
	}
	if err != nil {
		return err
	}
	return h.answerMembers(c, l, 0, false, nil)
}

func (h *handler) moveInList(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	sid, err := serviceParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	before, after, err := h.Lists.Move(ctx, l.ID, sid, c.FormValue("dir") == "down", events.ActorAdmin)
	if errors.Is(err, lists.ErrStaleOrder) {
		return h.answerMembers(c, l, 0, true, nil)
	}
	if err != nil {
		return err
	}
	undo, err := h.undo(ctx, l, before, after)
	if err != nil {
		return err
	}
	return h.answerMembers(c, l, sid, false, undo)
}

func (h *handler) orderList(c *echo.Context) error {
	l, err := h.loadList(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	ids, ok := parseCSV(c.FormValue("order"))
	if !ok {
		return h.answerMembers(c, l, 0, true, nil)
	}
	var expect []int64
	if e := c.FormValue("expect"); e != "" {
		if expect, ok = parseCSV(e); !ok {
			return h.answerMembers(c, l, 0, true, nil)
		}
	}
	before, after, err := h.Lists.SetOrder(ctx, l.ID, ids, expect, events.ActorAdmin)
	if errors.Is(err, lists.ErrStaleOrder) {
		return h.answerMembers(c, l, 0, true, nil)
	}
	if err != nil {
		return err
	}
	undo, err := h.undo(ctx, l, before, after)
	if err != nil {
		return err
	}
	return h.answerMembers(c, l, movedOne(order(before), order(after)), false, undo)
}

func order(r own.Result) []int64 {
	out := make([]int64, 0, len(r.Services))
	for _, o := range r.Services {
		out = append(out, o.ServiceID)
	}
	return out
}

// movedOne is the service a one-row move moved: the first that differs and
// went up, else the first that went down.
func movedOne(before, after []int64) int64 {
	for i := range after {
		if i < len(before) && before[i] != after[i] {
			if slices.Index(before, after[i]) > i {
				return after[i]
			}
			return before[i]
		}
	}
	return 0
}

// undo words the band after a reorder: what moved, the names that change
// owner, the targets that re-sync, and the order to go back to. A reorder
// that changed nothing has no band.
func (h *handler) undo(ctx context.Context, l lists.List, before, after own.Result) (*undoView, error) {
	b, a := order(before), order(after)
	if slices.Equal(b, a) {
		return nil, nil
	}
	tags := map[int64]string{}
	for _, o := range after.Services {
		tags[o.ServiceID] = o.Tag
	}
	u := &undoView{Order: csv(b), Expect: csv(a)}
	m := movedOne(b, a)
	i := slices.Index(a, m)
	switch {
	case slices.Index(b, m) > i && i+1 < len(a):
		u.Moved = i18n.T(ctx, "lists.undo.above", i18n.Args{"tag": tags[m], "other": tags[a[i+1]]})
	case i > 0:
		u.Moved = i18n.T(ctx, "lists.undo.below", i18n.Args{"tag": tags[m], "other": tags[a[i-1]]})
	default:
		u.Moved = i18n.T(ctx, "lists.undo.reordered")
	}
	moves := own.Moved(before, after)
	if len(moves) == 0 {
		u.Owners = i18n.T(ctx, "lists.undo.no_owner")
	} else {
		var names []string
		for _, mv := range moves {
			if len(names) == 2 {
				break
			}
			names = append(names, mv.Name)
		}
		more := ""
		if len(moves) > len(names) {
			more = "…"
		}
		u.Owners = i18n.N(ctx, "lists.undo.owners", int64(len(moves)), i18n.Args{"names": strings.Join(names, ", ") + more})
	}
	ts, err := h.Lists.Targets(ctx, l.ID)
	if err != nil {
		return nil, err
	}
	if len(ts) > 0 {
		var names []string
		for _, t := range ts {
			names = append(names, t.Name)
		}
		u.Targets = i18n.T(ctx, "lists.undo.targets", i18n.Args{"targets": andList(ctx, names)})
	}
	return u, nil
}

// andList is "Home", "Home and iPhone", "Home, Office and iPhone".
func andList(ctx context.Context, names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + i18n.T(ctx, "services.and") + " " + names[len(names)-1]
}

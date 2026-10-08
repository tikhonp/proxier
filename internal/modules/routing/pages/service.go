package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ------------------------------------------------------------ the page

type kv struct{ K, V string }

type domRow struct {
	Name, Unicode, Kind, Note string
}

type domainsView struct {
	ID       int64
	Q        string
	Rows     []domRow
	Info     string
	NextHref string
	PrevHref string
}

type skipRow struct{ Entry, Reason string }

type histRow struct {
	Time, State, Kind string
	Count, Change     string
	Href              string
}

type eventLine struct{ Time, Text string }

type pageView struct {
	It       services.Item
	Custom   bool
	Line     string // "v2fly:netflix · tag netflix · in no list"
	Band     string // after add or save
	Counts   string // "212 · 198 suffix, 14 exact · 3 skipped"
	Domains  domainsView
	Skipped  []skipRow
	Source   []kv
	Dropped  []droppedView
	History  []histRow
	Activity []eventLine
}

// droppedView is what a service lists but doesn't install in one list.
type droppedView struct {
	List, Href string
	Rows       []domRow
}

func (h *handler) page(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	acc, err := h.Services.Accepted(ctx, it.ID)
	if err != nil {
		return err
	}
	lists, err := h.Services.ListsOf(ctx, it.ID)
	if err != nil {
		return err
	}
	ms, err := h.Lists.Memberships(ctx, it.ID)
	if err != nil {
		return err
	}
	v := pageView{It: it, Custom: it.Source == selector.Custom}
	v.Line = sourceWord(ctx, it) + " · " + i18n.T(ctx, "services.tag_is", i18n.Args{"tag": it.Tag}) + " · " + inLists(ctx, lists)
	switch {
	case c.QueryParam("added") == "1":
		v.Band = i18n.N(ctx, "services.added_band", int64(acc.Count()), i18n.Args{"tag": it.Tag})
	case c.QueryParam("saved") == "1":
		v.Band = i18n.N(ctx, "services.saved_band", int64(acc.Count()), i18n.Args{"tag": it.Tag})
		if n, _ := strconv.Atoi(c.QueryParam("merged")); n > 0 {
			v.Band += " " + loc.N("services.saved_merged", int64(n))
		}
	case c.QueryParam("switched") == "1":
		v.Band = i18n.T(ctx, "services.switched_band", i18n.Args{"tag": it.Tag, "selector": it.Selector})
	}
	v.Counts = loc.Number(int64(acc.Count())) + " · " + i18n.T(ctx, "services.counts", i18n.Args{
		"suffix": loc.Number(int64(acc.SuffixCount)), "exact": loc.Number(int64(acc.ExactCount)),
	}) + " · " + loc.N("services.skipped_n", int64(len(acc.Set.Skipped)))
	if v.Domains, err = h.domainsView(ctx, it, acc, c.QueryParam("q"), pageOf(c)); err != nil {
		return err
	}
	for _, s := range acc.Set.Skipped {
		v.Skipped = append(v.Skipped, skipRow{Entry: s.Entry, Reason: i18n.T(ctx, s.Reason)})
	}
	v.Source = sourceRows(ctx, it, acc, ms)
	if v.Dropped, err = h.droppedViews(ctx, it, ms); err != nil {
		return err
	}
	hist, err := h.Services.Snapshots(ctx, it.ID)
	if err != nil {
		return err
	}
	for _, s := range hist {
		v.History = append(v.History, histRow{
			Time: loc.Time(s.FetchedAt), State: i18n.T(ctx, "services.snap."+s.Status), Kind: snapKind(s.Status),
			Count:  loc.Number(int64(s.Count())),
			Change: "+" + loc.Number(int64(s.Added)) + " −" + loc.Number(int64(s.Removed)),
			Href:   svcHref(it.ID) + "/snapshots/" + i64(s.ID),
		})
	}
	if v.Activity, err = h.activity(ctx, it); err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, servicePage(h.shell(c, it.Tag, listPath), v))
}

func snapKind(status string) string {
	switch status {
	case "accepted":
		return "ok"
	case "rejected":
		return "look"
	}
	return "off"
}

func sourceRows(ctx context.Context, it services.Item, acc services.Snapshot, ms []lists.Membership) []kv {
	loc := i18n.From(ctx)
	var out []kv
	if it.Source == selector.Custom {
		out = append(out, kv{i18n.T(ctx, "services.source.name"), it.Name})
		if it.Description != "" {
			out = append(out, kv{i18n.T(ctx, "services.source.description"), it.Description})
		}
	} else {
		out = append(out, kv{i18n.T(ctx, "services.source.selector"), it.Selector})
	}
	out = append(out, kv{i18n.T(ctx, "services.source.source"), string(it.Source)})
	if acc.Portal != "" {
		out = append(out, kv{i18n.T(ctx, "services.source.portal"), acc.Portal + " · " + i18n.T(ctx, "services.kind."+acc.Kind)})
	}
	out = append(out,
		kv{i18n.T(ctx, "services.source.created"), loc.Time(it.CreatedAt)},
		kv{i18n.T(ctx, "services.source.accepted"), loc.Time(acc.AcceptedAt) + " · " + loc.Number(int64(acc.Count()))},
	)
	l := i18n.T(ctx, "services.none")
	if len(ms) > 0 {
		var parts []string
		for _, m := range ms {
			parts = append(parts, m.List.Name+" #"+strconv.Itoa(m.Position))
		}
		l = strings.Join(parts, " · ")
	}
	return append(out, kv{i18n.T(ctx, "services.source.lists"), l})
}

// droppedViews are the names the service doesn't install, per list holding
// it, and why: ADR 0013's "owned elsewhere".
func (h *handler) droppedViews(ctx context.Context, it services.Item, ms []lists.Membership) ([]droppedView, error) {
	var out []droppedView
	for _, m := range ms {
		v, err := h.Lists.View(ctx, m.List.ID, nil)
		if err != nil {
			return nil, err
		}
		for _, mem := range v.Members {
			if mem.Service.ID != it.ID || len(mem.Owned.Dropped) == 0 {
				continue
			}
			dv := droppedView{List: m.List.Name, Href: listHref(m.List.ID)}
			for _, d := range mem.Owned.Dropped {
				if len(dv.Rows) == diffMax {
					break
				}
				r := domRow{Name: d.Name, Unicode: domain.Unicode(d.Name), Kind: i18n.T(ctx, "services.kind.suffix"), Note: dropReason(ctx, d, m.List.Name)}
				if d.Exact {
					r.Kind = i18n.T(ctx, "services.kind.exact")
				}
				dv.Rows = append(dv.Rows, r)
			}
			out = append(out, dv)
		}
	}
	return out, nil
}

// dropReason says why a name isn't installed: "covered by anthropic.com
// (anthropic)", "owned by anthropic (first in Main)", "left out: covers
// nl-1.hosts.tikhonnnnn.com (nl-1)".
func dropReason(ctx context.Context, d own.Drop, list string) string {
	return i18n.T(ctx, "lists.drop."+d.Reason, i18n.Args{"by": d.By, "via": d.Via, "list": list})
}

func (h *handler) activity(ctx context.Context, it services.Item) ([]eventLine, error) {
	loc := i18n.From(ctx)
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: services.Subject(it.ID), Limit: 10})
	if err != nil {
		return nil, err
	}
	var out []eventLine
	for _, e := range list {
		args := i18n.Args{"subject": it.Tag, "actor": e.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		text := e.Type
		key := "event." + e.Type
		if e.Type == "routing.service_updated" {
			key = "services.event.updated." + firstChange(e.Payload)
		}
		if loc.Has(key) {
			text = loc.T(key, args)
		}
		out = append(out, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return out, nil
}

// firstChange picks the wording of a service_updated line.
func firstChange(p map[string]any) string {
	ch, _ := p["changes"].(string)
	switch {
	case ch == "source":
		return "source"
	case strings.Contains(ch, "tag"):
		return "tag"
	case strings.Contains(ch, "domains"):
		return "domains"
	}
	return "fields"
}

// domainsView pages the accepted snapshot's names, filtered by q.
func (h *handler) domainsView(ctx context.Context, it services.Item, acc services.Snapshot, q string, p int) (domainsView, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	notes := map[string]string{}
	if it.Source == selector.Custom {
		rows, err := h.Services.CustomRows(ctx, it.ID)
		if err != nil {
			return domainsView{}, err
		}
		for _, r := range rows {
			notes[r.Domain] = r.Note
		}
	}
	type name struct {
		n     string
		exact bool
	}
	var all []name
	for _, n := range acc.Set.Suffix {
		all = append(all, name{n, false})
	}
	for _, n := range acc.Set.Exact {
		all = append(all, name{n, true})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].n < all[j].n })
	var kept []name
	for _, n := range all {
		if q == "" || strings.Contains(n.n, q) || strings.Contains(domain.Unicode(n.n), q) {
			kept = append(kept, n)
		}
	}
	v := domainsView{ID: it.ID, Q: q}
	from, to := window(len(kept), p)
	for _, n := range kept[from:to] {
		r := domRow{Name: n.n, Unicode: domain.Unicode(n.n), Kind: i18n.T(ctx, "services.kind.suffix"), Note: notes[n.n]}
		if n.exact {
			r.Kind = i18n.T(ctx, "services.kind.exact")
		}
		if under := ownSuffix(acc.Set, n.n, n.exact); under != "" && r.Note == "" {
			r.Note = i18n.T(ctx, "services.under", i18n.Args{"name": under})
		}
		v.Rows = append(v.Rows, r)
	}
	if len(kept) > 0 {
		v.Info = i18n.T(ctx, "services.pager", i18n.Args{"from": from + 1, "to": to, "n": len(kept)})
	} else if q != "" {
		v.Info = i18n.T(ctx, "services.domains.no_match")
	}
	page := func(n int) string {
		vals := url.Values{"page": {strconv.Itoa(n)}}
		if q != "" {
			vals.Set("q", q)
		}
		return svcHref(it.ID) + "/domains?" + vals.Encode()
	}
	if to < len(kept) {
		v.NextHref = page(p + 1)
	}
	if p > 1 && from > 0 {
		v.PrevHref = page(p - 1)
	}
	return v, nil
}

// ownSuffix is the topmost suffix of the same set above name ("" for none):
// an upstream service keeps such names (mtvpn compatibility).
func ownSuffix(s snapshot.Set, name string, exact bool) string {
	under := ""
	if exact && s.Has(name, false) {
		return name
	}
	for _, p := range domain.Parents(name) {
		if s.Has(p, false) {
			under = p
		}
	}
	return under
}

func (h *handler) domains(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	acc, err := h.Services.Accepted(ctx, it.ID)
	if err != nil {
		return err
	}
	v, err := h.domainsView(ctx, it, acc, c.QueryParam("q"), pageOf(c))
	if err != nil {
		return err
	}
	if !htmx(c) {
		q := url.Values{}
		if v.Q != "" {
			q.Set("q", v.Q)
		}
		if p := pageOf(c); p > 1 {
			q.Set("page", strconv.Itoa(p))
		}
		to := svcHref(it.ID)
		if len(q) > 0 {
			to += "?" + q.Encode()
		}
		return web.Redirect(c, to+"#domains")
	}
	return web.Render(c, http.StatusOK, domainsList(v))
}

// ------------------------------------------------------------ switch

type switchView struct {
	It       services.Item
	Selector string
	Err      string
}

func (h *handler) switchPage(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	if it.Source == selector.Custom {
		return echo.ErrNotFound
	}
	sel := c.QueryParam("selector")
	if sel == "" {
		sel = it.Selector
	}
	return h.renderSwitch(c, http.StatusOK, switchView{It: it, Selector: sel})
}

func (h *handler) renderSwitch(c *echo.Context, status int, v switchView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, switchPage(h.shell(c, i18n.T(ctx, "services.switch.title", i18n.Args{"tag": v.It.Tag}), listPath), v))
}

func (h *handler) switchPost(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	raw := strings.TrimSpace(c.FormValue("selector"))
	err = h.Services.Switch(ctx, it.ID, raw, events.ActorAdmin)
	if errors.Is(err, services.ErrCustom) {
		return echo.ErrNotFound
	}
	if err == nil {
		return web.Redirect(c, svcHref(it.ID)+"?switched=1")
	}
	msg, ok := selectorError(ctx, raw, err)
	if !ok {
		return err
	}
	return h.renderSwitch(c, http.StatusUnprocessableEntity, switchView{It: it, Selector: raw, Err: msg})
}

// ------------------------------------------------------------ the diff

type diffLine struct{ Name, Unicode, Kind string }

type diffView struct {
	It                     services.Item
	Title, Line            string
	Added, Removed         []diffLine
	MoreAdded, MoreRemoved string
	AddedHead, RemovedHead string
}

const diffMax = 500

func (h *handler) diff(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	snap, err := strconv.ParseInt(c.Param("snap"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	s, prev, err := h.Services.Snapshot(ctx, it.ID, snap)
	if errors.Is(err, services.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	d := snapshot.Compare(prev.Set, s.Set)
	v := diffView{It: it, Title: i18n.T(ctx, "services.diff.title", i18n.Args{"time": loc.Time(s.FetchedAt)})}
	v.Line = i18n.T(ctx, "services.snap."+s.Status) + " · " + loc.Number(int64(s.Set.Count())) + " · +" +
		loc.Number(int64(d.Added())) + " −" + loc.Number(int64(d.Removed()))
	if prev.ID == 0 {
		v.Line += " · " + i18n.T(ctx, "services.diff.first")
	} else {
		v.Line += " · " + i18n.T(ctx, "services.diff.against", i18n.Args{"time": loc.Time(prev.FetchedAt)})
	}
	v.AddedHead = i18n.T(ctx, "services.diff.added", i18n.Args{"n": d.Added()})
	v.RemovedHead = i18n.T(ctx, "services.diff.removed", i18n.Args{"n": d.Removed()})
	v.Added, v.MoreAdded = diffLines(ctx, d.AddedSuffix, d.AddedExact)
	v.Removed, v.MoreRemoved = diffLines(ctx, d.RemovedSuffix, d.RemovedExact)
	return web.Render(c, http.StatusOK, diffPage(h.shell(c, it.Tag, listPath), v))
}

func diffLines(ctx context.Context, suffix, exact []string) ([]diffLine, string) {
	var out []diffLine
	for _, n := range suffix {
		out = append(out, diffLine{Name: n, Unicode: domain.Unicode(n), Kind: i18n.T(ctx, "services.kind.suffix")})
	}
	for _, n := range exact {
		out = append(out, diffLine{Name: n, Unicode: domain.Unicode(n), Kind: i18n.T(ctx, "services.kind.exact")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > diffMax {
		return out[:diffMax], i18n.T(ctx, "services.diff.more", i18n.Args{"n": len(out) - diffMax})
	}
	return out, ""
}

// ------------------------------------------------------------ remove

type removeView struct {
	It    services.Item
	Lists []listLink
	Err   string
}

type listLink struct{ Name, Href string }

func (h *handler) removePage(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	links, err := h.listLinks(c.Request().Context(), it.ID)
	if err != nil {
		return err
	}
	return h.renderRemove(c, http.StatusOK, removeView{It: it, Lists: links})
}

func (h *handler) listLinks(ctx context.Context, id int64) ([]listLink, error) {
	ms, err := h.Lists.Memberships(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []listLink
	for _, m := range ms {
		out = append(out, listLink{Name: m.List.Name, Href: listHref(m.List.ID)})
	}
	return out, nil
}

func (h *handler) renderRemove(c *echo.Context, status int, v removeView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, removePage(h.shell(c, i18n.T(ctx, "services.remove.title", i18n.Args{"tag": v.It.Tag}), listPath), v))
}

func (h *handler) removePost(c *echo.Context) error {
	it, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	err = h.Services.Remove(ctx, it.ID, events.ActorAdmin)
	var in *services.InListsError
	if errors.As(err, &in) {
		links, lerr := h.listLinks(ctx, it.ID)
		if lerr != nil {
			return lerr
		}
		return h.renderRemove(c, http.StatusConflict, removeView{It: it, Lists: links, Err: i18n.T(ctx, "services.remove.in_lists")})
	}
	if errors.Is(err, services.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, listPath)
}

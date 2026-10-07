package pages

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const activityPageSize = 50

type actRow struct {
	Time    string
	Actor   string
	Pre     string
	Subject ui.SubjectRef
	Post    string
	Plain   string // the whole sentence when it has no subject slot
}

type actDay struct {
	Day  string
	Rows []actRow
}

type activityView struct {
	Days  []actDay
	Chips []ui.Chip
	Form  *ui.FilterForm
	Clear string
	Info  string
	Older string
}

type actFilter struct {
	Module, Type, Subject, Actor, Range string
}

func (f actFilter) query() url.Values {
	v := url.Values{}
	for k, x := range map[string]string{"module": f.Module, "type": f.Type, "subject": f.Subject, "actor": f.Actor, "range": f.Range} {
		if x != "" {
			v.Set(k, x)
		}
	}
	return v
}

func (f actFilter) active() bool { return len(f.query()) > 0 }

func rangeSince(r string) time.Time {
	switch r {
	case "24h":
		return time.Now().Add(-24 * time.Hour)
	case "7d":
		return time.Now().Add(-7 * 24 * time.Hour)
	case "30d":
		return time.Now().Add(-30 * 24 * time.Hour)
	}
	return time.Time{}
}

// sentinel marks where the subject link goes inside a translated sentence.
const sentinel = "\x00"

func (h *handler) activityPage(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	q := c.Request().URL.Query()
	f := actFilter{Module: q.Get("module"), Type: q.Get("type"), Subject: q.Get("subject"), Actor: q.Get("actor"), Range: q.Get("range")}
	if f.Range != "24h" && f.Range != "7d" && f.Range != "30d" {
		f.Range = ""
	}
	filter := events.Filter{Module: f.Module, Type: f.Type, Actor: f.Actor, Since: rangeSince(f.Range), Limit: activityPageSize + 1}
	filter.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	if t, id, ok := strings.Cut(f.Subject, ":"); ok {
		filter.Subject = events.Subject{Type: t, ID: id}
	}
	list, err := events.List(ctx, h.Query, filter)
	if err != nil {
		return err
	}
	v := activityView{}
	if len(list) > activityPageSize {
		list = list[:activityPageSize]
		o := f.query()
		o.Set("before", strconv.FormatInt(list[len(list)-1].ID, 10))
		v.Older = "/activity?" + o.Encode()
	}
	subs := make([]events.Subject, len(list))
	for i, e := range list {
		subs[i] = e.Subject
	}
	named := h.names.resolve(ctx, subs)

	var cur *actDay
	for _, e := range list {
		day := loc.Day(e.Time.Time)
		if cur == nil || cur.Day != day {
			v.Days = append(v.Days, actDay{Day: day})
			cur = &v.Days[len(v.Days)-1]
		}
		row := actRow{Time: loc.Clock(e.Time.Time)[:5], Actor: actorLabel(ctx, e.Actor)}
		ref := named[e.Subject]
		args := i18n.Args{"subject": sentinel, "actor": row.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		key := "event." + e.Type
		text := e.Type
		if loc.Has(key) {
			text = loc.T(key, args)
		}
		if pre, post, ok := strings.Cut(text, sentinel); ok && e.Subject.Type != "" {
			row.Pre, row.Post, row.Subject = pre, post, ref
		} else {
			row.Plain = strings.ReplaceAll(text, sentinel, ref.Label)
			if e.Subject.Type != "" && ref.Label != "" {
				row.Subject = ref
				row.Pre = row.Plain + " "
				row.Plain = ""
			}
		}
		cur.Rows = append(cur.Rows, row)
	}

	chip := func(label, r string) ui.Chip {
		g := f
		g.Range = r
		href := "/activity"
		if qs := g.query().Encode(); qs != "" {
			href += "?" + qs
		}
		return ui.Chip{Label: label, Href: href, On: f.Range == r}
	}
	v.Chips = []ui.Chip{chip(i18n.T(ctx, "activity.range.all"), ""), chip(i18n.T(ctx, "activity.range.24h"), "24h"),
		chip(i18n.T(ctx, "activity.range.7d"), "7d"), chip(i18n.T(ctx, "activity.range.30d"), "30d")}

	modOpts := []ui.FilterOption{{Value: "", Label: i18n.T(ctx, "activity.module_all")}}
	typeOpts := []ui.FilterOption{{Value: "", Label: i18n.T(ctx, "activity.type_all")}}
	seen := map[string]bool{}
	for _, t := range h.Events.Types() {
		if !seen[t.Module] {
			seen[t.Module] = true
			modOpts = append(modOpts, ui.FilterOption{Value: t.Module, Label: t.Module})
		}
		typeOpts = append(typeOpts, ui.FilterOption{Value: t.Name, Label: t.Name})
	}
	hidden := map[string]string{}
	for _, k := range []string{"subject", "actor", "range"} {
		if x := q.Get(k); x != "" && (k != "range" || f.Range != "") {
			hidden[k] = x
		}
	}
	v.Form = &ui.FilterForm{Action: "/activity", Hidden: hidden, Apply: i18n.T(ctx, "jobs.filter.apply"), Selects: []ui.FilterSelect{
		{Name: "module", Label: i18n.T(ctx, "activity.module"), Value: f.Module, Options: modOpts},
		{Name: "type", Label: i18n.T(ctx, "activity.type"), Value: f.Type, Options: typeOpts},
	}}
	if f.active() {
		v.Clear = "/activity"
	}
	n := 0
	for _, d := range v.Days {
		n += len(d.Rows)
	}
	v.Info = i18n.N(ctx, "activity.shown", int64(n))
	return web.Render(c, http.StatusOK, activityPage(h.shell(c, i18n.T(ctx, "nav.activity"), "/activity"), v))
}

func actorLabel(ctx context.Context, actor string) string {
	if id, ok := strings.CutPrefix(actor, "job:"); ok {
		return i18n.T(ctx, "activity.actor.job", i18n.Args{"id": id})
	}
	return actor
}

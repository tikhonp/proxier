package pages

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/stats"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type statCell struct{ Label, Value, Sub string }

type chartView struct {
	ID, Title string
	Last      string
	Series    []ui.Series
	Range     ui.Range
	Legend    []string // a name per series, when there are several
	Gaps      bool     // the series has a break the chart shows
	Axis      []string
}

type containerRow struct {
	Name, State, Image string
	Restarts           int
	Kind               string
}

type statsView struct {
	serverView
	Range      string
	Ranges     []ui.Chip
	HasData    bool
	SampleAt   string
	Now        []statCell
	Charts     []chartView
	Containers []containerRow
}

func (h *handler) statsTab(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	if s.State != "active" || s.Retiring() {
		return web.Redirect(c, serverHref(s.ID))
	}
	ctx := c.Request().Context()
	v, err := h.buildStats(ctx, s, stats.ParseRange(c.QueryParam("range")))
	if err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, statsPage(h.shell(c, s.Name+" · "+i18n.T(ctx, "servers.tab.stats"), "/servers"), v))
}

// HumanBytes writes a size with a binary unit.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func humanUptime(ctx context.Context, secs float64) string {
	return i18n.From(ctx).Duration(time.Duration(secs) * time.Second)
}

func (h *handler) buildStats(ctx context.Context, s store.Server, r stats.Range) (statsView, error) {
	loc := i18n.From(ctx)
	v := statsView{serverView: h.headView(ctx, s), Range: string(r)}
	for _, rr := range []stats.Range{stats.Range24h, stats.Range7d, stats.Range30d} {
		v.Ranges = append(v.Ranges, ui.Chip{Label: i18n.T(ctx, "health.range."+string(rr)), Href: serverHref(s.ID) + "/stats?range=" + string(rr), On: rr == r})
	}
	series, err := h.Stats.Series(ctx, s.ID, r)
	if err != nil {
		return v, err
	}
	cur, ok, err := h.Stats.Latest(ctx, s.ID)
	if err != nil {
		return v, err
	}
	v.HasData = ok
	if !ok {
		return v, nil
	}
	v.SampleAt = loc.Ago(cur.Sample.At.Time)
	for _, ct := range cur.Containers {
		kind := map[string]string{"running": "ok", "restarting": "look", "exited": "broken", "dead": "broken", "paused": "paused"}[ct.State]
		if kind == "" {
			kind = "unknown"
		}
		v.Containers = append(v.Containers, containerRow{Name: ct.Name, State: ct.State, Image: ct.Image, Restarts: ct.Restarts, Kind: kind})
	}
	rxToday, txToday, err := h.Stats.TrafficToday(ctx, s.ID)
	if err != nil {
		return v, err
	}
	rxMonth, txMonth, err := h.Stats.TrafficMonth(ctx, s.ID)
	if err != nil {
		return v, err
	}
	p := cur.Point
	cpu := "—"
	if p.CPU != nil {
		cpu = fmt.Sprintf("%.0f %%", *p.CPU)
	}
	v.Now = []statCell{
		{i18n.T(ctx, "stats.cpu"), cpu, ""},
		{i18n.T(ctx, "stats.load"), fmt.Sprintf("%.2f", p.Load1), ""},
		{i18n.T(ctx, "stats.memory"), fmt.Sprintf("%.0f %%", p.MemPct), HumanBytes(p.MemUsed) + " / " + HumanBytes(p.MemTotal)},
		{i18n.T(ctx, "stats.disk"), fmt.Sprintf("%.0f %%", p.DiskPct), HumanBytes(p.DiskUsed) + " / " + HumanBytes(p.DiskTotal)},
		{i18n.T(ctx, "stats.traffic_today"), HumanBytes(rxToday + txToday), "↓ " + HumanBytes(rxToday) + " · ↑ " + HumanBytes(txToday)},
		{i18n.T(ctx, "stats.traffic_month"), HumanBytes(rxMonth + txMonth), "↓ " + HumanBytes(rxMonth) + " · ↑ " + HumanBytes(txMonth)},
		{i18n.T(ctx, "stats.uptime"), humanUptime(ctx, cur.Sample.Uptime), ""},
	}

	layout := "15:04"
	if r != stats.Range24h {
		layout = "2 Jan"
	}
	var axis []string
	for i := 0; i <= 4; i++ {
		t := series.From.Add(series.To.Sub(series.From) * time.Duration(i) / 4)
		axis = append(axis, t.In(loc.TZ).Format(layout))
	}
	rng := func(label string, max float64) ui.Range {
		return ui.Range{From: series.From, To: series.To, Gap: series.Gap, Max: max, Label: label}
	}
	line := func(name, class string, f func(stats.Point) *float64) ui.Series {
		sr := ui.Series{Name: name, Class: class}
		for _, pt := range series.Points {
			if val := f(pt); val != nil {
				sr.Points = append(sr.Points, ui.P(pt.At, *val))
			} else {
				sr.Points = append(sr.Points, ui.Point{At: pt.At})
			}
		}
		return sr
	}
	num := func(x float64) *float64 { return &x }
	bytesOf := func(x *int64) *float64 {
		if x == nil {
			return nil
		}
		return num(float64(*x))
	}
	gaps := func(sr ...ui.Series) bool {
		for _, one := range sr {
			for i := 1; i < len(one.Points); i++ {
				if one.Points[i].At.Sub(one.Points[i-1].At) > series.Gap {
					return true
				}
			}
		}
		return false
	}
	add := func(id, title, last string, max float64, sr ...ui.Series) {
		cv := chartView{ID: id, Title: title, Last: last, Series: sr, Range: rng(title, max), Axis: axis, Gaps: gaps(sr...)}
		if len(sr) > 1 {
			for _, one := range sr {
				cv.Legend = append(cv.Legend, one.Name)
			}
		}
		v.Charts = append(v.Charts, cv)
	}
	add("cpu", i18n.T(ctx, "stats.cpu"), cpu, 100, line("cpu", "", func(pt stats.Point) *float64 { return pt.CPU }))
	add("load", i18n.T(ctx, "stats.load"), fmt.Sprintf("%.2f", p.Load1), 0, line("load", "", func(pt stats.Point) *float64 { return num(pt.Load1) }))
	add("memory", i18n.T(ctx, "stats.memory"), fmt.Sprintf("%.0f %%", p.MemPct), 100, line("memory", "", func(pt stats.Point) *float64 { return num(pt.MemPct) }))
	add("disk", i18n.T(ctx, "stats.disk"), fmt.Sprintf("%.0f %%", p.DiskPct), 100, line("disk", "", func(pt stats.Point) *float64 { return num(pt.DiskPct) }))
	per := i18n.T(ctx, "stats.per_sample")
	if series.Hourly {
		per = i18n.T(ctx, "stats.per_hour")
	}
	add("traffic", i18n.T(ctx, "stats.traffic")+" · "+per, "", 0,
		line(i18n.T(ctx, "stats.rx"), "", func(pt stats.Point) *float64 { return bytesOf(pt.RX) }),
		line(i18n.T(ctx, "stats.tx"), "chart-b", func(pt stats.Point) *float64 { return bytesOf(pt.TX) }))
	return v, nil
}

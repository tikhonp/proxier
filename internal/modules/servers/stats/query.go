package stats

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Range is how far back a chart looks.
type Range string

// The ranges of the Stats tab: raw samples for 24 h and 7 d, hours for 30 d.
const (
	Range24h Range = "24h"
	Range7d  Range = "7d"
	Range30d Range = "30d"
)

// ParseRange reads ?range=; anything unknown is 24 h.
func ParseRange(s string) Range {
	switch Range(s) {
	case Range7d, Range30d:
		return Range(s)
	}
	return Range24h
}

// Duration is how far back the range reaches.
func (r Range) Duration() time.Duration {
	switch r {
	case Range7d:
		return 7 * 24 * time.Hour
	case Range30d:
		return 30 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// Point is one sample (or one hour) of a chart. A nil CPU, RX or TX is a
// value that was not known, not zero.
type Point struct {
	At        time.Time
	Load1     float64
	CPU       *float64
	MemPct    float64
	DiskPct   float64
	RX, TX    *int64
	MemUsed   int64
	MemTotal  int64
	DiskUsed  int64
	DiskTotal int64
}

// Series is what the charts draw. Points are oldest first; a missing sample is
// no point at all, and two points further apart than Gap are not joined.
type Series struct {
	Range    Range
	From, To time.Time
	Gap      time.Duration
	Hourly   bool
	Points   []Point
}

func pct(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func fromSample(s store.Sample) Point {
	p := Point{At: s.At.Time, Load1: s.Load1, MemPct: pct(s.MemUsed, s.MemTotal), DiskPct: pct(s.DiskUsed, s.DiskTotal),
		MemUsed: s.MemUsed, MemTotal: s.MemTotal, DiskUsed: s.DiskUsed, DiskTotal: s.DiskTotal}
	if s.CPUPct.Valid {
		v := s.CPUPct.Float64
		p.CPU = &v
	}
	if s.RXBytes.Valid {
		v := s.RXBytes.Int64
		p.RX = &v
	}
	if s.TXBytes.Valid {
		v := s.TXBytes.Int64
		p.TX = &v
	}
	return p
}

func fromHourly(h store.Hourly) Point {
	p := Point{At: h.Hour.Time, Load1: h.Load1, MemPct: pct(h.MemUsed, h.MemTotal), DiskPct: pct(h.DiskUsed, h.DiskTotal),
		MemUsed: h.MemUsed, MemTotal: h.MemTotal, DiskUsed: h.DiskUsed, DiskTotal: h.DiskTotal}
	if h.CPUPct.Valid {
		v := h.CPUPct.Float64
		p.CPU = &v
	}
	rx, tx := h.RXBytes, h.TXBytes
	p.RX, p.TX = &rx, &tx
	return p
}

// interval is how often a self-check runs (the setting).
func (s *Service) interval(ctx context.Context) time.Duration {
	d, err := s.Settings.GetDuration(ctx, conf.SelfcheckEvery)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

// Series returns a server's samples for a range. The 30 d range is hourly:
// rolled-up hours plus the raw samples not yet rolled, averaged per hour.
func (s *Service) Series(ctx context.Context, serverID int64, r Range) (Series, error) {
	now := s.Now().UTC()
	from := now.Add(-r.Duration())
	out := Series{Range: r, From: from, To: now}
	every := s.interval(ctx)
	raw, err := store.Samples(ctx, s.DB.R, serverID, db.At(from), db.At(now))
	if err != nil {
		return out, err
	}
	if r != Range30d {
		out.Gap = 2 * every
		for _, sm := range raw {
			out.Points = append(out.Points, fromSample(sm))
		}
		return out, nil
	}
	out.Hourly, out.Gap = true, 2*time.Hour
	hours, err := store.HourlyRows(ctx, s.DB.R, serverID, db.At(from.Truncate(time.Hour)), db.At(now))
	if err != nil {
		return out, err
	}
	seen := map[time.Time]bool{}
	for _, h := range hours {
		seen[h.Hour.Time] = true
		out.Points = append(out.Points, fromHourly(h))
	}
	// the raw samples of hours not rolled up yet, averaged per hour
	type acc struct {
		n         int
		load, cpu float64
		cpuN      int
		mem, disk float64
		rx, tx    int64
		last      store.Sample
	}
	byHour := map[time.Time]*acc{}
	var order []time.Time
	for _, sm := range raw {
		h := sm.At.Truncate(time.Hour)
		if seen[h] {
			continue
		}
		a := byHour[h]
		if a == nil {
			a = &acc{}
			byHour[h] = a
			order = append(order, h)
		}
		a.n++
		a.load += sm.Load1
		if sm.CPUPct.Valid {
			a.cpu += sm.CPUPct.Float64
			a.cpuN++
		}
		a.mem += pct(sm.MemUsed, sm.MemTotal)
		a.disk += pct(sm.DiskUsed, sm.DiskTotal)
		a.rx += sm.RXBytes.Int64
		a.tx += sm.TXBytes.Int64
		a.last = sm
	}
	for _, h := range order {
		a := byHour[h]
		p := Point{At: h, Load1: a.load / float64(a.n), MemPct: a.mem / float64(a.n), DiskPct: a.disk / float64(a.n),
			MemUsed: a.last.MemUsed, MemTotal: a.last.MemTotal, DiskUsed: a.last.DiskUsed, DiskTotal: a.last.DiskTotal}
		if a.cpuN > 0 {
			v := a.cpu / float64(a.cpuN)
			p.CPU = &v
		}
		rx, tx := a.rx, a.tx
		p.RX, p.TX = &rx, &tx
		out.Points = append(out.Points, p)
	}
	sortPoints(out.Points)
	return out, nil
}

func sortPoints(p []Point) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && p[j].At.Before(p[j-1].At); j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}

// zone is the display time zone.
func (s *Service) zone(ctx context.Context) *time.Location {
	if tz, err := s.Settings.Get(ctx, "general.time_zone"); err == nil {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.UTC
}

// TrafficToday is the traffic since local midnight in the display time zone.
func (s *Service) TrafficToday(ctx context.Context, serverID int64) (rx, tx int64, err error) {
	loc := s.zone(ctx)
	n := s.Now().In(loc)
	return s.trafficSince(ctx, serverID, time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc))
}

// TrafficMonth is the traffic since local midnight on the first of the month.
func (s *Service) TrafficMonth(ctx context.Context, serverID int64) (rx, tx int64, err error) {
	loc := s.zone(ctx)
	n := s.Now().In(loc)
	return s.trafficSince(ctx, serverID, time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc))
}

func (s *Service) trafficSince(ctx context.Context, serverID int64, from time.Time) (int64, int64, error) {
	t, err := store.TrafficSince(ctx, s.DB.R, serverID, db.At(from))
	return t.RX, t.TX, err
}

// Current is the newest sample of a server with its containers decoded.
type Current struct {
	Sample     store.Sample
	Point      Point
	Containers []remote.Container
}

// Latest returns the server's newest sample.
func (s *Service) Latest(ctx context.Context, serverID int64) (Current, bool, error) {
	sm, ok, err := store.LastSample(ctx, s.DB.R, serverID)
	if err != nil || !ok {
		return Current{}, false, err
	}
	c := Current{Sample: sm, Point: fromSample(sm)}
	_ = json.Unmarshal([]byte(sm.Containers), &c.Containers)
	return c, true, nil
}

package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Point is one sample of a series; a nil V is a missing sample (a gap in the
// line, never a zero).
type Point struct {
	At time.Time
	V  *float64
}

// P makes a point with a value.
func P(at time.Time, v float64) Point { return Point{At: at, V: &v} }

// Series is one line of a chart. Class picks its stroke (chart-a, chart-b).
type Series struct {
	Name   string
	Class  string
	Points []Point
}

// Range is the time axis of a chart and what counts as a gap: two points
// further apart than Gap are not joined. Max fixes the top of the value axis
// (0: the largest value, at least 1); Min is the bottom.
type Range struct {
	From, To time.Time
	Gap      time.Duration
	Min, Max float64
	// Label is the accessible description of the chart.
	Label string
}

// Chart geometry: the viewBox, so the SVG scales with its container.
const (
	chartW = 600
	chartH = 120
	chartP = 4 // inner padding, so a line at the extremes is not clipped
)

// linePaths turns points into SVG path data, one subpath per run of points
// that are present and close enough to each other: missing samples and long
// silences break the line. A run of one point is drawn as a dot (a zero-length
// segment with round caps).
func linePaths(pts []Point, r Range, w, h float64) string {
	span := r.To.Sub(r.From).Seconds()
	if span <= 0 {
		return ""
	}
	max := r.Max
	if max <= r.Min {
		max = r.Min + 1
		for _, p := range pts {
			if p.V != nil && *p.V > max {
				max = *p.V
			}
		}
	}
	x := func(t time.Time) float64 {
		return chartP + (w-2*chartP)*t.Sub(r.From).Seconds()/span
	}
	y := func(v float64) float64 {
		f := (v - r.Min) / (max - r.Min)
		f = math.Max(0, math.Min(1, f))
		return h - chartP - (h-2*chartP)*f
	}
	var b strings.Builder
	var prev *Point
	for i := range pts {
		p := pts[i]
		if p.V == nil || p.At.Before(r.From) || p.At.After(r.To) {
			prev = nil
			continue
		}
		cmd := "L"
		if prev == nil || (r.Gap > 0 && p.At.Sub(prev.At) > r.Gap) {
			cmd = "M"
		}
		fmt.Fprintf(&b, "%s%.1f %.1f", cmd, x(p.At), y(*p.V))
		if cmd == "M" {
			// a lone sample still shows: a zero-length segment
			fmt.Fprintf(&b, "h0")
		}
		prev = &pts[i]
	}
	return b.String()
}

// Segments reports how many separate subpaths a chart line has (tests).
func Segments(path string) int { return strings.Count(path, "M") }

// barWidth is the filled share of a bar, in percent.
func barPct(used, total int64) int {
	if total <= 0 || used <= 0 {
		return 0
	}
	p := int(math.Round(float64(used) / float64(total) * 100))
	if p > 100 {
		p = 100
	}
	return p
}

// barKind colours a bar by how full it is: calm, look, broken.
func barKind(pct int) string {
	switch {
	case pct >= 90:
		return "bar-bad"
	case pct >= 75:
		return "bar-warn"
	}
	return "bar-ok"
}

func viewBox(w, h int) string { return "0 0 " + strconv.Itoa(w) + " " + strconv.Itoa(h) }

// StripCell values.
const (
	CellOK   = "ok"
	CellFail = "fail"
	CellNone = "none"
)

func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

// Segment is a stretch of a timeline in one state (a status kind: ok look
// blocked broken unknown paused).
type Segment struct {
	Kind     string
	From, To time.Time
	Title    string // already translated, shown on hover
}

// segmentGeom is where a segment sits on a 1000-wide axis.
func segmentGeom(s Segment, from, to time.Time) (x, w float64, ok bool) {
	span := to.Sub(from).Seconds()
	if span <= 0 {
		return 0, 0, false
	}
	a, b := s.From, s.To
	if a.Before(from) {
		a = from
	}
	if b.After(to) {
		b = to
	}
	if !b.After(a) {
		return 0, 0, false
	}
	x = 1000 * a.Sub(from).Seconds() / span
	w = 1000 * b.Sub(a).Seconds() / span
	if w < 2 {
		w = 2
	}
	return x, w, true
}

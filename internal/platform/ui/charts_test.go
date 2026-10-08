package ui_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/ui"
)

func TestChartsBreakOnNull(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }
	gap := ui.Point{At: at(10)} // a missing sample
	pts := []ui.Point{ui.P(at(0), 1), ui.P(at(5), 2), gap, ui.P(at(15), 3), ui.P(at(20), 2),
		// 40 minutes of silence, longer than the gap limit: no line across it
		ui.P(at(60), 1), ui.P(at(65), 1)}
	r := ui.Range{From: t0, To: at(70), Gap: 12 * time.Minute, Max: 4, Label: "cpu"}

	var b bytes.Buffer
	if err := ui.TimeSeries([]ui.Series{{Name: "cpu", Points: pts}}, r).Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "style=") {
		t.Errorf("inline style in %s", out)
	}
	if !strings.Contains(out, `<svg class="chart"`) || !strings.Contains(out, `class="chart-line `) {
		t.Fatalf("not a chart: %s", out)
	}
	i := strings.Index(out, `class="chart-line `)
	d := out[i:]
	d = d[strings.Index(d, `d="`)+3:]
	d = d[:strings.Index(d, `"`)]
	if n := ui.Segments(d); n != 3 {
		t.Errorf("want 3 subpaths (missing sample and long silence break the line), got %d in %q", n, d)
	}
	if strings.Contains(d, "NaN") {
		t.Errorf("NaN in %q", d)
	}

	// Nothing at all: an empty path, not a flat line at zero.
	b.Reset()
	if err := ui.Sparkline([]ui.Point{{At: at(1)}, {At: at(2)}}, r).Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "M") && strings.Contains(b.String(), `d="M`) {
		t.Errorf("a series of gaps drew a line: %s", b.String())
	}

	b.Reset()
	if err := ui.Bar(95, 100).Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "bar-bad") || strings.Contains(b.String(), "style=") {
		t.Errorf("bar: %s", b.String())
	}
}

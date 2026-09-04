// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"testing"
	"time"

	"github.com/go-aiquota/tray/history"
	"github.com/go-widgets/toolkit"
)

const (
	chartTestW = 100
	chartTestH = 50
)

// pixelAt reads one RGBA pixel out of a RenderChart buffer.
func pixelAt(t *testing.T, buf []byte, w, x, y int) (r, g, b, a uint8) {
	t.Helper()
	i := (y*w + x) * 4
	if i < 0 || i+3 >= len(buf) {
		t.Fatalf("pixel (%d,%d) is out of bounds for a %dx%d buffer", x, y, w, chartTestH)
	}
	return buf[i], buf[i+1], buf[i+2], buf[i+3]
}

func TestRenderChartRejectsNonPositiveSize(t *testing.T) {
	if _, err := RenderChart(0, 10, nil, time.Now()); err == nil {
		t.Fatal("RenderChart(0, 10, ...): want an error, got nil")
	}
	if _, err := RenderChart(10, -1, nil, time.Now()); err == nil {
		t.Fatal("RenderChart(10, -1, ...): want an error, got nil")
	}
}

func TestRenderChartWithNoDataStillReturnsAPanel(t *testing.T) {
	buf, err := RenderChart(chartTestW, chartTestH, nil, time.Now())
	if err != nil {
		t.Fatalf("RenderChart(nil series): %v, want a blank panel, not an error", err)
	}
	if len(buf) != chartTestW*chartTestH*4 {
		t.Fatalf("len(buf) = %d, want %d", len(buf), chartTestW*chartTestH*4)
	}
}

// TestRenderChartDrawsARisingSessionLine is the actual load-bearing proof:
// a session series that climbs from 10% to 90% must draw a line whose
// point is higher on screen (smaller Y) later in time than earlier — not
// just "a chart that doesn't error".
func TestRenderChartDrawsARisingSessionLine(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	series := map[string][]history.Point{
		"session": {
			{AtUnix: now.Add(-time.Hour).Unix(), Used: 10, Limit: 100},
			{AtUnix: now.Unix(), Used: 90, Limit: 100},
		},
	}
	buf, err := RenderChart(chartTestW, chartTestH, series, now)
	if err != nil {
		t.Fatal(err)
	}

	// Scan each column in the plot area for the topmost ink-colored pixel
	// (the line's y at that x) — a coarse but real reconstruction of the
	// drawn curve, not a hardcoded single-pixel probe that would break if
	// the stroke's anti-aliasing shifted by one pixel.
	ink := seriesInk["session"]
	topAt := func(x int) (y int, found bool) {
		for y := 0; y < chartTestH; y++ {
			r, g, b, a := pixelAt(t, buf, chartTestW, x, y)
			if a > 128 && closeColor(r, g, b, ink) {
				return y, true
			}
		}
		return 0, false
	}

	leftY, leftOK := topAt(chartMargin + 2)
	rightY, rightOK := topAt(chartTestW - chartMargin - 2)
	if !leftOK || !rightOK {
		t.Fatalf("did not find the session line near both edges (left found=%v, right found=%v)", leftOK, rightOK)
	}
	if rightY >= leftY {
		t.Fatalf("line y at right (%d) is not above y at left (%d) — a rising series should draw a line climbing toward the top", rightY, leftY)
	}
}

// TestRenderChartGivesEachSeriesItsOwnTimeAxis is the actual proof behind
// RenderChart's own doc comment: a session series spanning a few hours
// must reach all the way across the plot width, the SAME as a weekly
// series spanning several days does — neither gets crushed by sharing an
// axis with the other. Caught live: an earlier version shared one axis
// and the hours-scale line was squeezed into an unreadable sliver next
// to the days-scale one.
func TestRenderChartGivesEachSeriesItsOwnTimeAxis(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	// Deliberately different percentages (not just different time spans)
	// so the two lines never cross paths at the sampled x — a shared pixel
	// would have the later-drawn series occlude the earlier one there,
	// which is a test-collision artifact, not evidence about the axis.
	series := map[string][]history.Point{
		"session": { // spans 2 hours, climbs low
			{AtUnix: now.Add(-2 * time.Hour).Unix(), Used: 5, Limit: 100},
			{AtUnix: now.Unix(), Used: 35, Limit: 100},
		},
		"weekly": { // spans 6 days — 72x wider a range — climbs high
			{AtUnix: now.Add(-6 * 24 * time.Hour).Unix(), Used: 65, Limit: 100},
			{AtUnix: now.Unix(), Used: 95, Limit: 100},
		},
	}
	buf, err := RenderChart(chartTestW, chartTestH, series, now)
	if err != nil {
		t.Fatal(err)
	}

	// Both series' leftmost point should sit near the plot's left edge —
	// not just the weekly (wide-range) one.
	leftX := chartMargin + 2
	for _, key := range []string{"session", "weekly"} {
		ink := seriesInk[key]
		found := false
		for y := 0; y < chartTestH; y++ {
			r, g, b, a := pixelAt(t, buf, chartTestW, leftX, y)
			if a > 128 && closeColor(r, g, b, ink) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: no ink found near the left edge (x=%d) — its own axis should start there regardless of the other series' range", key, leftX)
		}
	}
}

func TestRenderChartTwoSeriesUseDifferentColors(t *testing.T) {
	now := time.Now()
	series := map[string][]history.Point{
		"session": {{AtUnix: now.Add(-time.Hour).Unix(), Used: 50, Limit: 100}, {AtUnix: now.Unix(), Used: 50, Limit: 100}},
		"weekly":  {{AtUnix: now.Add(-time.Hour).Unix(), Used: 50, Limit: 100}, {AtUnix: now.Unix(), Used: 50, Limit: 100}},
	}
	sessionInk := seriesInk["session"]
	weeklyInk := seriesInk["weekly"]
	if sessionInk == weeklyInk {
		t.Fatal("session and weekly must not share a color, or the two lines would be indistinguishable")
	}
	// Both series are flat at 50%, so RenderChart must not error drawing
	// two overlapping-height lines at once.
	if _, err := RenderChart(chartTestW, chartTestH, series, now); err != nil {
		t.Fatal(err)
	}
}

func TestRenderChartSkipsASeriesWithFewerThanTwoPoints(t *testing.T) {
	now := time.Now()
	series := map[string][]history.Point{
		"session": {{AtUnix: now.Unix(), Used: 50, Limit: 100}}, // just one point
	}
	// Must not panic or error over a series with nothing to connect.
	if _, err := RenderChart(chartTestW, chartTestH, series, now); err != nil {
		t.Fatal(err)
	}
}

func TestRenderChartSkipsPointsWithNoLimit(t *testing.T) {
	now := time.Now()
	series := map[string][]history.Point{
		// Both points are "n/a" (Limit <= 0) — chartPath must not divide
		// by zero building the line, and must treat this the same as
		// having fewer than two USABLE points.
		"session": {
			{AtUnix: now.Add(-time.Hour).Unix(), Used: 0, Limit: 0},
			{AtUnix: now.Unix(), Used: 0, Limit: 0},
		},
	}
	if _, err := RenderChart(chartTestW, chartTestH, series, now); err != nil {
		t.Fatal(err)
	}
}

func TestRenderChartIgnoresAnUnrecognizedSeriesKey(t *testing.T) {
	now := time.Now()
	series := map[string][]history.Point{
		"weekly_scoped": {
			{AtUnix: now.Add(-time.Hour).Unix(), Used: 10, Limit: 100},
			{AtUnix: now.Unix(), Used: 90, Limit: 100},
		},
	}
	buf, err := RenderChart(chartTestW, chartTestH, series, now)
	if err != nil {
		t.Fatal(err)
	}
	// Every pixel should be background or gridline — nothing drawn for a
	// key seriesInk doesn't recognize.
	for y := 0; y < chartTestH; y++ {
		for x := 0; x < chartTestW; x++ {
			r, g, b, _ := pixelAt(t, buf, chartTestW, x, y)
			if !closeColor(r, g, b, chartBackground) && !closeColor(r, g, b, chartGridline) {
				t.Fatalf("pixel (%d,%d) = (%d,%d,%d), want background or gridline only", x, y, r, g, b)
			}
		}
	}
}

// closeColor allows a few units of anti-aliasing/rounding slack rather
// than demanding an exact byte match.
func closeColor(r, g, b uint8, want toolkit.RGBA) bool {
	const tol = 10
	d := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	return d(r, want.R) <= tol && d(g, want.G) <= tol && d(b, want.B) <= tol
}

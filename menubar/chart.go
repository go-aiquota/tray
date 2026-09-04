// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package menubar

import (
	"fmt"
	"sort"
	"time"

	"github.com/go-aiquota/tray/history"
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// chartMargin keeps a curve from being drawn flush against the panel's own
// edge, where an anti-aliased stroke's outer half would be clipped.
const chartMargin = 8

// chartLineWidth is the stroke width of one series' curve, in pixels.
const chartLineWidth = 2

// seriesInk assigns each history series (see history.ClassifyWindow's
// keys via menubar.ClassifyWindow) a fixed color, reusing dotInk's own
// palette (icon.go) rather than inventing a second one — a color means
// the same thing in the chart as it does in the status dot.
var seriesInk = map[string]toolkit.RGBA{
	"session": dotInk[SeverityOK],
	"weekly":  dotInk[SeverityWarning],
}

// chartBackground and chartGridline are the panel's own chrome — neither
// is a status color, so they don't come from dotInk.
var (
	chartBackground = toolkit.RGB(0xFA, 0xFA, 0xFA)
	chartGridline   = toolkit.RGB(0xDD, 0xDD, 0xDD)
)

// RenderChart draws one polyline per entry in series onto a w×h panel,
// each point plotted 0% at the bottom to 100% at the top (climbing usage
// draws a climbing line) by Used/Limit. Returns RGBA bytes (w*h*4, ready
// for toolkit.NewImage) — the only error is a non-positive w or h.
//
// Each series gets ITS OWN x-axis, spanning from its own earliest point
// through now — not one axis shared across every series. Session and
// weekly are different-cadence quotas (hours versus days between
// resets); sharing one axis was tried first and measured live: weekly's
// multi-day span crushed session's few-hour span into an unreadable
// sliver at the right edge. A person reading "how has my SESSION quota
// moved since it last reset" and "how has my WEEKLY quota moved since
// its own last reset" is asking two questions with two different natural
// time windows, not one synchronized timeline — an axis per series
// answers the actual question instead of forcing a shared one that
// answers neither well.
//
// A series with fewer than two usable points (nothing to connect — a
// brand-new account, or every point's Limit <= 0, an "n/a" window per
// windowRow) draws nothing for that series rather than a degenerate
// zero-length line; the panel itself (background + gridlines) is always
// returned, so a caller always has something to show. An unrecognized
// series key (not one seriesInk knows a color for) is skipped rather
// than drawn in a guessed color.
func RenderChart(w, h int, series map[string][]history.Point, now time.Time) ([]byte, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("menubar: a chart of %dx%d pixels", w, h)
	}
	buf := make([]byte, w*h*4)
	p := painter.NewPixelPainterBGRA(buf, w, h)
	p.FillRect(toolkit.Rect{W: w, H: h}, chartBackground)

	plotX, plotY := chartMargin, chartMargin
	plotW, plotH := w-2*chartMargin, h-2*chartMargin
	if plotW <= 0 || plotH <= 0 {
		return bgraToRGBA(buf), nil
	}

	for _, frac := range []float64{0, 0.5, 1} { // 0%, 50%, 100% gridlines
		y := plotY + int(float64(plotH)*(1-frac))
		p.FillRect(toolkit.Rect{X: plotX, Y: y, W: plotW, H: 1}, chartGridline)
	}

	for _, key := range sortedSeriesKeys(series) { // stable draw order; map iteration isn't
		ink, known := seriesInk[key]
		if !known {
			continue
		}
		if path := chartPath(series[key], now.Unix(), plotX, plotY, plotW, plotH); path != nil {
			p.StrokePath(path, ink, chartLineWidth)
		}
	}

	return bgraToRGBA(buf), nil
}

// chartPath builds one series' polyline, in panel pixel coordinates,
// against ITS OWN time range (earliest usable point through now — see
// RenderChart's own doc comment for why not a shared one). Only points
// with a real Limit contribute — an "n/a" window's Used/Limit would
// divide by zero — and fewer than two of those leaves nothing to
// connect, or a degenerate range (every usable point at exactly now), so
// it returns nil rather than a single MoveTo with no LineTo.
func chartPath(pts []history.Point, now int64, x, y, w, h int) *painter.Path {
	valid := make([]history.Point, 0, len(pts))
	minT := now
	for _, pt := range pts {
		if pt.Limit <= 0 {
			continue
		}
		valid = append(valid, pt)
		if pt.AtUnix < minT {
			minT = pt.AtUnix
		}
	}
	if len(valid) < 2 || minT >= now {
		return nil
	}
	path := painter.NewPath()
	span := float64(now - minT)
	for i, pt := range valid {
		frac := float64(pt.AtUnix-minT) / span
		px := float64(x) + frac*float64(w)
		pct := pt.Used / pt.Limit
		pct = min(1, max(0, pct))
		py := float64(y) + (1-pct)*float64(h)
		if i == 0 {
			path.MoveTo(px, py)
		} else {
			path.LineTo(px, py)
		}
	}
	return path
}

func sortedSeriesKeys(series map[string][]history.Point) []string {
	keys := make([]string, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

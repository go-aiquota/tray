// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package menubar's chart-related metadata backs go-aiquota/tray's
// per-account "View history…" window (cmd/aiquota-tray/history_window.go):
// this file names and colors the two series every account's history
// tracks, so the window's toolkit.TimeSeriesChart panels and legend agree
// with each other. The chart itself is drawn by
// github.com/go-widgets/toolkit's TimeSeriesChart widget — a REUSABLE
// widget, not something hand-rolled here, after an earlier version of
// this file did exactly that (raw pixel painting, including axis-label
// text via painter.PixelPainter's own built-in font — a prototype-scoped
// font missing lowercase letters, which is why a month abbreviation like
// "Jan" rendered broken) and was replaced once the gap in the shared
// toolkit was filled properly instead.
package menubar

import (
	"time"

	"github.com/go-widgets/toolkit"
)

// chartSeriesOrder is the fixed, stable order every history chart panel
// (and a caller's legend) lists series in — not whatever order a map
// happens to range over, so panels never reshuffle between renders and a
// legend row always lines up with the panel it describes.
var chartSeriesOrder = []string{"session", "weekly"}

// SeriesOrder returns the fixed display order a caller (the history
// window's panels and legend) should use.
func SeriesOrder() []string {
	out := make([]string, len(chartSeriesOrder))
	copy(out, chartSeriesOrder)
	return out
}

// seriesLabelText is the human-facing caption for each series key.
var seriesLabelText = map[string]string{
	"session": "Session",
	"weekly":  "Weekly",
}

// SeriesLabel returns the human-facing caption for a series key (e.g.
// "Session" for "session") — the text a legend shows next to
// SeriesColor's swatch.
func SeriesLabel(key string) (string, bool) {
	l, ok := seriesLabelText[key]
	return l, ok
}

// seriesInk assigns each history series (see ClassifyWindow's keys) a
// fixed color, reusing dotInk's own palette (icon.go) rather than
// inventing a second one — a color means the same thing in the chart as
// it does in the status dot.
var seriesInk = map[string]toolkit.RGBA{
	"session": dotInk[SeverityOK],
	"weekly":  dotInk[SeverityWarning],
}

// SeriesColor returns the fixed color a history series draws in — the
// color both a toolkit.TimeSeriesChart's Ink and a legend swatch should
// use, so the two agree with each other by construction rather than by
// two independently-chosen palettes staying in sync.
func SeriesColor(key string) (toolkit.RGBA, bool) {
	c, ok := seriesInk[key]
	return c, ok
}

// seriesWindowDuration is each series' own window length — session
// resets on a rolling 5h cycle, weekly on a 7-day one, per the product's
// own documented limits (quotapb.QuotaWindow's Label field comment: "e.g.
// rolling 5h" for session). Neither is reported explicitly by a provider
// (QuotaWindow carries only ResetsAtUnix, not how long the window
// leading up to it lasted), so these are a fixed, documented assumption
// rather than a value read off the wire — good enough for a pace
// REFERENCE line, not a claim about the provider's exact contract.
var seriesWindowDuration = map[string]time.Duration{
	"session": 5 * time.Hour,
	"weekly":  7 * 24 * time.Hour,
}

// SeriesWindowDuration returns how long a series' own window runs before
// resetting — what a sustainable-pace reference line (see
// history_window.go) divides elapsed time by to get "how far through the
// window am I."
func SeriesWindowDuration(key string) (time.Duration, bool) {
	d, ok := seriesWindowDuration[key]
	return d, ok
}

// OverPaceColor is the color a history chart's curve switches to for any
// stretch where usage is running ahead of the sustainable pace that would
// exhaust the window exactly at its reset — reusing SeverityCritical's
// own red rather than inventing a second "something's wrong" color, since
// that is exactly what it means: unchanged pace runs the account dry
// before the window resets.
func OverPaceColor() toolkit.RGBA {
	return dotInk[SeverityCritical]
}

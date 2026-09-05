// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-aiquota/tray/history"
	"github.com/go-aiquota/tray/menubar"
	"github.com/go-widgets/toolkit"
	"github.com/go-widgets/window"
)

const (
	historyChartWidth   = 400
	historyPanelHeight  = 100
	historyLegendHeight = 28
	historySwatchPx     = 12
)

// openHistoryWindowsMu guards openHistoryWindowsM, read by
// RefreshHistoryWindows (main.go's poll loop, one goroutine) and written
// by openHistoryWindow's own goroutine (whichever one app.Loop dispatched
// ActionViewHistory onto) — the same two-goroutine shape recordHistory
// and refresh() already share elsewhere in this package.
var (
	openHistoryWindowsMu sync.Mutex
	openHistoryWindowsM  = map[string]*historyWindowHandle{}
)

// historyWindowHandle is what RefreshHistoryWindows needs to repaint an
// already-open window: one toolkit.TimeSeriesChart per series, whose
// Points a fresh poll's data replaces, and the window.Repainter that
// makes the change actually show up (see window.Repainter's own doc
// comment: without an explicit repaint request, a window "draws its
// first frame and, with the window idle, would show it for as long as
// it runs" — exactly what a static history-chart window would otherwise
// do forever).
type historyWindowHandle struct {
	charts  map[string]*toolkit.TimeSeriesChart
	backend window.Backend
}

// openHistoryWindow renders accountLabel's recent session/weekly history
// from store into a small native window — one toolkit.TimeSeriesChart
// panel per series (see historyRoot) plus a legend, real toolkit widgets
// throughout rather than anything hand-painted — and blocks until it's
// closed, the same open/run/close shape onboarding's window uses.
// Dispatched through app.Loop like every other action (see
// app.Handlers.OnViewHistory), so it already runs with the tray's own
// Hold released, exactly as addAccount's onboarding window does.
//
// While open, it's registered so RefreshHistoryWindows (called from
// main.go's refresh(), once per poll) can update it with whatever's new
// — a window left open shows the account's usage moving, not a snapshot
// frozen at the moment it was opened.
func openHistoryWindow(store *history.Store, accountID, accountLabel string) error {
	charts := buildCharts(store, accountID)

	win, err := window.Open(window.Config{
		Title:  accountLabel + " — usage history",
		Width:  historyChartWidth,
		Height: historyPanelHeight*len(menubar.SeriesOrder()) + historyLegendHeight,
	})
	if err != nil {
		return fmt.Errorf("opening the history window: %w", err)
	}
	defer win.Close()

	openHistoryWindowsMu.Lock()
	openHistoryWindowsM[accountID] = &historyWindowHandle{charts: charts, backend: win}
	openHistoryWindowsMu.Unlock()
	// Removed before win.Close() runs (defers are LIFO: this one was
	// registered after win.Close()'s, so it fires first) — a repaint can
	// never be dispatched to a backend that's already gone.
	defer func() {
		openHistoryWindowsMu.Lock()
		delete(openHistoryWindowsM, accountID)
		openHistoryWindowsMu.Unlock()
	}()

	return win.Run(historyRoot(charts))
}

// RefreshHistoryWindows re-reads every currently-open history window's
// series from store, updates its charts' Points in place, and repaints.
// Called once per poll (main.go's refresh(), right after recordHistory)
// rather than on an independent timer: there is no new data to show
// between polls, so refreshing more often would just repaint the same
// points.
func RefreshHistoryWindows(store *history.Store) {
	openHistoryWindowsMu.Lock()
	defer openHistoryWindowsMu.Unlock()
	for accountID, h := range openHistoryWindowsM {
		series := historySeriesFor(store, accountID)
		for key, chart := range h.charts {
			chart.Points = toChartPoints(series[key])
		}
		if r, ok := h.backend.(window.Repainter); ok {
			r.Repaint()
		}
	}
}

// buildCharts constructs one toolkit.TimeSeriesChart per series (see
// menubar.SeriesOrder), fixed to a 0-100% value scale — a percentage
// chart's whole point is that fixed scale, not one that rescales to
// whatever the account's own data happens to reach.
func buildCharts(store *history.Store, accountID string) map[string]*toolkit.TimeSeriesChart {
	series := historySeriesFor(store, accountID)
	charts := make(map[string]*toolkit.TimeSeriesChart, len(menubar.SeriesOrder()))
	for _, key := range menubar.SeriesOrder() {
		c := toolkit.NewTimeSeriesChart(toChartPoints(series[key]), 0, 100)
		if ink, ok := menubar.SeriesColor(key); ok {
			c.Ink = ink
		}
		c.FormatValue = func(v float64) string { return fmt.Sprintf("%.0f%%", v) }
		charts[key] = c
	}
	return charts
}

// toChartPoints converts history.Points into toolkit.TimePoints as a
// percentage (Used/Limit*100) — the unit buildCharts' fixed 0-100 Y
// bounds assume. A point with no real Limit (an "n/a" window per
// windowRow) is dropped rather than dividing by zero.
func toChartPoints(pts []history.Point) []toolkit.TimePoint {
	out := make([]toolkit.TimePoint, 0, len(pts))
	for _, p := range pts {
		if p.Limit <= 0 {
			continue
		}
		out = append(out, toolkit.TimePoint{At: p.AtUnix, Value: p.Used / p.Limit * 100})
	}
	return out
}

// historySeriesFor reads accountID's session/weekly points back out of
// store, going back as far as the store retains (history.Retention).
func historySeriesFor(store *history.Store, accountID string) map[string][]history.Point {
	since := time.Now().Add(-history.Retention)
	series := make(map[string][]history.Point, len(menubar.SeriesOrder()))
	for _, key := range menubar.SeriesOrder() {
		pts, err := store.Recent(accountID, key, since)
		if err != nil {
			log.Printf("aiquota-tray: reading %s history for %s: %v", key, accountID, err)
			continue
		}
		series[key] = pts
	}
	return series
}

// historyRoot stacks one chart panel per series (see menubar.SeriesOrder)
// above a legend row — real toolkit widgets throughout (Backdrop color
// swatches, Label captions, mirroring go-xrkit/desk/settings.go's own
// BoxLayout+Container+Label composition), not text hand-painted into a
// pixel buffer. Swatch colors come from menubar.SeriesColor — the exact
// color each chart's own Ink already draws in, not a second,
// independently-chosen palette that could drift from it.
func historyRoot(charts map[string]*toolkit.TimeSeriesChart) toolkit.Widget {
	root := toolkit.NewContainer(&toolkit.BoxLayout{Vertical: true})
	legend := toolkit.NewContainer(&toolkit.BoxLayout{Spacing: 6})
	for _, key := range menubar.SeriesOrder() {
		if c, ok := charts[key]; ok {
			root.Add(toolkit.Item{Widget: c, Size: historyPanelHeight})
		}
		ink, inkOK := menubar.SeriesColor(key)
		label, labelOK := menubar.SeriesLabel(key)
		if !inkOK || !labelOK {
			continue
		}
		legend.Add(toolkit.Item{Size: historySwatchPx, Widget: toolkit.NewBackdrop(ink, toolkit.RGBA{}, 0)})
		legend.Add(toolkit.Item{Widget: toolkit.NewLabel(label), Natural: true})
	}
	root.Add(toolkit.Item{Widget: toolkit.NewPadding(legend, 8), Size: historyLegendHeight})
	return root
}
